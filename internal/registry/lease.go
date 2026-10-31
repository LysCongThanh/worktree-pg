package registry

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/LysCongThanh/worktree-pg/internal/errs"
)

// LeaseTTL bounds a lease whose liveness cannot be proven by start time (non-Linux).
const LeaseTTL = 2 * time.Hour

// NewLease describes the current process performing op.
func NewLease(op string, now time.Time) *Lease {
	host, _ := os.Hostname()
	return &Lease{PID: os.Getpid(), PIDStart: ProcStart(os.Getpid()), Host: host, Op: op, Since: now}
}

// Stale reports whether the lease holder is gone: different PID start time or a
// dead PID (Linux), or kill -0 failure / TTL expiry elsewhere. Leases from another
// host are only stale after the TTL.
func (l *Lease) Stale(now time.Time) bool {
	if l == nil {
		return true
	}
	host, _ := os.Hostname()
	if l.Host != host {
		return now.Sub(l.Since) > LeaseTTL
	}
	if !pidAlive(l.PID) {
		return true
	}
	if l.PIDStart != "" {
		return ProcStart(l.PID) != l.PIDStart
	}
	return now.Sub(l.Since) > LeaseTTL
}

// Acquire sets a lease on w for op. A live lease held by another process fails
// with Busy/LEASE_HELD (exit 6). It returns whether a stale lease was replaced, so
// the caller can roll back that holder's recorded intent.
func (w *Worktree) Acquire(op string, now time.Time) (replacedStale bool, err error) {
	if w.Lease != nil && !w.Lease.Stale(now) && !w.Lease.self() {
		return false, errs.New(errs.Busy, errs.CodeLeaseHeld, "worktree %s is busy: %s by pid %d since %s",
			w.Path, w.Lease.Op, w.Lease.PID, w.Lease.Since.Format(time.RFC3339)).
			WithHint("wait for it to finish or retry with --wait")
	}
	replacedStale = w.Lease != nil && !w.Lease.self()
	w.Lease = NewLease(op, now)
	return replacedStale, nil
}

// Release clears the lease if this process holds it.
func (w *Worktree) Release() {
	if w.Lease != nil && w.Lease.self() {
		w.Lease = nil
	}
}

// self reports whether the lease belongs to this process.
func (l *Lease) self() bool {
	return l.PID == os.Getpid() && l.PIDStart == ProcStart(os.Getpid())
}

// pidAlive uses kill(pid, 0): nil or EPERM means the process exists.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// ProcStart returns the process start time (clock ticks since boot, field 22 of
// /proc/<pid>/stat) on Linux, "" elsewhere or when unreadable. It defeats PID reuse.
func ProcStart(pid int) string {
	if runtime.GOOS != "linux" {
		return ""
	}
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return ""
	}
	return ParseStatStart(string(b))
}

// ParseStatStart extracts starttime from /proc/<pid>/stat content. The comm field
// may contain spaces and parentheses, so parsing starts after the last ')'.
func ParseStatStart(stat string) string {
	i := strings.LastIndexByte(stat, ')')
	if i < 0 {
		return ""
	}
	f := strings.Fields(stat[i+1:])
	// fields after comm start at field 3 (state); starttime is field 22 -> index 19
	if len(f) < 20 {
		return ""
	}
	return f[19]
}
