// Package ports allocates sticky, collision-free dev ports per worktree.
//
// Allocation is deterministic (sha256 start + linear probe) and excludes ports
// taken by other worktrees, reserved ports, privileged ports, the kernel's
// ephemeral range and ports bound by docker containers (invariant S8).
package ports

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

// Range is an inclusive TCP port range.
type Range struct{ Lo, Hi int }

// DefaultEphemeral is Linux's default ip_local_port_range, used when the system value is unreadable.
var DefaultEphemeral = Range{32768, 60999}

// String renders "lo-hi".
func (r Range) String() string { return fmt.Sprintf("%d-%d", r.Lo, r.Hi) }

// Size is the number of ports in the range.
func (r Range) Size() int { return r.Hi - r.Lo + 1 }

// Contains reports whether p is inside the range.
func (r Range) Contains(p int) bool { return p >= r.Lo && p <= r.Hi }

// Overlaps reports whether two ranges share at least one port.
func (r Range) Overlaps(o Range) bool { return r.Lo <= o.Hi && o.Lo <= r.Hi }

// ParseRange parses "5600-5999" or a single port "5600".
func ParseRange(s string) (Range, error) {
	lo, hi, found := strings.Cut(strings.TrimSpace(s), "-")
	a, err1 := strconv.Atoi(strings.TrimSpace(lo))
	b := a
	var err2 error
	if found {
		b, err2 = strconv.Atoi(strings.TrimSpace(hi))
	}
	if err1 != nil || err2 != nil {
		return Range{}, fmt.Errorf("port range %q: want N or N-M", s)
	}
	r := Range{a, b}
	if a < 1024 || b > 65535 || a > b {
		return Range{}, fmt.Errorf("port range %q: must satisfy 1024 <= lo <= hi <= 65535", s)
	}
	return r, nil
}

// ParseRanges parses a list of range strings.
func ParseRanges(list []string) ([]Range, error) {
	out := make([]Range, 0, len(list))
	for _, s := range list {
		r, err := ParseRange(s)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// PortSet is a set of excluded ports.
type PortSet map[int]struct{}

// Add inserts ports into the set (allocating it if needed) and returns it.
func (s PortSet) Add(ps ...int) PortSet {
	if s == nil {
		s = PortSet{}
	}
	for _, p := range ps {
		s[p] = struct{}{}
	}
	return s
}

// Has reports membership; safe on a nil set.
func (s PortSet) Has(p int) bool { _, ok := s[p]; return ok }

// ParseProcRange parses the content of /proc/sys/net/ipv4/ip_local_port_range ("32768\t60999\n").
func ParseProcRange(content string) (Range, error) {
	f := strings.Fields(content)
	if len(f) != 2 {
		return Range{}, fmt.Errorf("unexpected ip_local_port_range %q", content)
	}
	lo, err1 := strconv.Atoi(f[0])
	hi, err2 := strconv.Atoi(f[1])
	if err1 != nil || err2 != nil || lo > hi {
		return Range{}, fmt.Errorf("unexpected ip_local_port_range %q", content)
	}
	return Range{lo, hi}, nil
}

// Ephemeral returns the kernel's ephemeral port range (Linux /proc, macOS sysctl),
// falling back to DefaultEphemeral when it cannot be read.
func Ephemeral() Range {
	switch runtime.GOOS {
	case "linux":
		if b, err := os.ReadFile("/proc/sys/net/ipv4/ip_local_port_range"); err == nil {
			if r, err := ParseProcRange(string(b)); err == nil {
				return r
			}
		}
	case "darwin", "freebsd":
		out, err := exec.Command("sysctl", "-n", "net.inet.ip.portrange.first", "net.inet.ip.portrange.last").Output()
		if err == nil {
			if r, err := ParseProcRange(string(out)); err == nil {
				return r
			}
		}
	}
	return DefaultEphemeral
}
