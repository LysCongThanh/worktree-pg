//go:build unix

// Package lockfile provides advisory flock(2) locks with a timeout. Locks are
// released automatically by the kernel when the holding process dies, so a crash
// never leaves the registry locked.
package lockfile

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/LysCongThanh/worktree-pg/internal/errs"
)

// pollInterval is how often a contended lock is retried.
const pollInterval = 25 * time.Millisecond

// Lock is a held flock on an open file.
type Lock struct{ f *os.File }

// Acquire takes an exclusive (or shared) flock on path, creating it with mode
// 0600 if needed. It retries until timeout and then fails with Busy/LOCK_TIMEOUT
// (exit 6). Context cancellation aborts the wait.
func Acquire(ctx context.Context, path string, exclusive bool, timeout time.Duration) (*Lock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, errs.Wrap(errs.Internal, errs.CodeInternal, err, "create lock dir")
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, errs.Wrap(errs.Internal, errs.CodeInternal, err, "open lock %s", path)
	}
	how := syscall.LOCK_SH
	if exclusive {
		how = syscall.LOCK_EX
	}
	deadline := time.Now().Add(timeout)
	for {
		err := syscall.Flock(int(f.Fd()), how|syscall.LOCK_NB)
		if err == nil {
			return &Lock{f: f}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			f.Close()
			return nil, errs.Wrap(errs.Internal, errs.CodeInternal, err, "flock %s", path)
		}
		if time.Now().After(deadline) {
			f.Close()
			return nil, errs.New(errs.Busy, errs.CodeLockTimeout, "timed out after %s waiting for lock %s", timeout, path).
				WithHint("another worktree-pg process is running; retry shortly")
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

// Release unlocks and closes the file. Safe to call on nil and more than once.
func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	err := l.f.Close()
	l.f = nil
	return err
}
