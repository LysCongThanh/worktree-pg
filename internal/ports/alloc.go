package ports

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"sort"

	"github.com/LysCongThanh/worktree-pg/internal/errs"
)

// DefaultKey is the Ranges map key holding the ranges used by vars without an override.
const DefaultKey = ""

// Allocator picks ports for one worktree. It holds no I/O itself: Probe is the
// seam used to test whether a port is currently free (nil = always free).
type Allocator struct {
	Ranges    map[string][]Range // per-var ranges; DefaultKey = default ranges
	Exclude   PortSet            // ports taken by other worktrees, reserved, docker-bound
	Ephemeral Range              // kernel ephemeral range (never allocated)
	Probe     func(port int) bool
}

// Warning is a non-fatal allocation note (e.g. a sticky port that is busy right now).
type Warning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// CodePortBusy is emitted when a sticky port is currently in use by another process.
const CodePortBusy = "PORT_BUSY"

// Allocate returns a port for each var. Ports already in sticky stay assigned
// (warning PORT_BUSY if the probe fails, never reassigned implicitly); new vars get
// start = sha256(key+"\x00"+var) mod size and a linear probe over the concatenated
// ranges. Exhaustion is a config error (exit 3).
func (a *Allocator) Allocate(key string, vars []string, sticky map[string]int) (map[string]int, []Warning, error) {
	sorted := append([]string(nil), vars...)
	sort.Strings(sorted)
	out := make(map[string]int, len(sorted))
	taken := PortSet{}
	var warns []Warning
	for _, v := range sorted { // keep valid sticky ports first so new vars avoid them
		p, ok := sticky[v]
		if !ok || !a.inRanges(v, p) || a.excluded(p) || taken.Has(p) {
			continue
		}
		out[v] = p
		taken.Add(p)
		if !a.free(p) {
			warns = append(warns, Warning{CodePortBusy, fmt.Sprintf("%s=%d is in use by another process; keeping it (use --reassign-ports to change)", v, p)})
		}
	}
	for _, v := range sorted {
		if _, done := out[v]; done {
			continue
		}
		p, err := a.pick(key, v, taken)
		if err != nil {
			return nil, warns, err
		}
		out[v] = p
		taken.Add(p)
	}
	return out, warns, nil
}

// pick runs the deterministic linear probe for one var.
func (a *Allocator) pick(key, v string, taken PortSet) (int, error) {
	rs := a.rangesFor(v)
	size := 0
	for _, r := range rs {
		size += r.Size()
	}
	if size == 0 {
		return 0, errs.New(errs.Config, errs.CodePortsExhausted, "no port range configured for %s", v)
	}
	start := int(Start(key, v) % uint64(size))
	for i := 0; i < size; i++ {
		p := nth(rs, (start+i)%size)
		if a.excluded(p) || taken.Has(p) || !a.free(p) {
			continue
		}
		return p, nil
	}
	return 0, errs.New(errs.Config, errs.CodePortsExhausted, "no free port for %s in %v", v, rs).
		WithHint("widen [ports].ranges or run `worktree-pg gc` to release ports of removed worktrees")
}

// Start is the deterministic probe origin for (key, var): first 8 bytes of sha256, big endian.
func Start(key, v string) uint64 {
	sum := sha256.Sum256([]byte(key + "\x00" + v))
	return binary.BigEndian.Uint64(sum[:8])
}

// nth returns the i-th port of the concatenated ranges.
func nth(rs []Range, i int) int {
	for _, r := range rs {
		if i < r.Size() {
			return r.Lo + i
		}
		i -= r.Size()
	}
	panic("ports: index out of range") // unreachable: callers bound i by total size
}

func (a *Allocator) rangesFor(v string) []Range {
	if rs, ok := a.Ranges[v]; ok && len(rs) > 0 {
		return rs
	}
	return a.Ranges[DefaultKey]
}

func (a *Allocator) inRanges(v string, p int) bool {
	for _, r := range a.rangesFor(v) {
		if r.Contains(p) {
			return true
		}
	}
	return false
}

// excluded applies the static exclusions: <1024, ephemeral range, Exclude set.
func (a *Allocator) excluded(p int) bool {
	if p < 1024 || p > 65535 || a.Exclude.Has(p) {
		return true
	}
	return a.Ephemeral.Hi > 0 && a.Ephemeral.Contains(p)
}

func (a *Allocator) free(p int) bool { return a.Probe == nil || a.Probe(p) }
