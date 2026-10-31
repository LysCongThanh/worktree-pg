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

// Lock is a held flock on an open file.
type Lock struct{ f *os.File }

// Acquire takes an exclusive (or shared) flock on path, creating it with mode
// 0600 if needed. A contended lock is waited for with a blocking flock (the kernel
// wakes waiters on release, so many contenders do not starve each other the way
// sleep-and-retry polling does). After timeout it fails with Busy/LOCK_TIMEOUT
// (exit 6); context cancellation aborts the wait.
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
	fd := int(f.Fd())
	err = flock(fd, how|syscall.LOCK_NB)
	if err == nil {
		return &Lock{f: f}, nil
	}
	if !errors.Is(err, syscall.EWOULDBLOCK) {
		f.Close()
		return nil, errs.Wrap(errs.Internal, errs.CodeInternal, err, "flock %s", path)
	}
	done := make(chan error, 1)
	go func() { done <- flock(fd, how) }()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-done:
		if err != nil {
			f.Close()
			return nil, errs.Wrap(errs.Internal, errs.CodeInternal, err, "flock %s", path)
		}
		return &Lock{f: f}, nil
	case <-timer.C:
		abandon(f, fd, done)
		return nil, errs.New(errs.Busy, errs.CodeLockTimeout, "timed out after %s waiting for lock %s", timeout, path).
			WithHint("another worktree-pg process is running; retry shortly")
	case <-ctx.Done():
		abandon(f, fd, done)
		return nil, ctx.Err()
	}
}

// flock retries flock(2) on EINTR (the Go runtime preempts with signals).
func flock(fd, how int) error {
	for {
		err := syscall.Flock(fd, how)
		if !errors.Is(err, syscall.EINTR) {
			return err
		}
	}
}

// abandon lets a waiter that gave up finish in the background: when its blocking
// flock eventually succeeds the lock is released at once, then the file closed.
func abandon(f *os.File, fd int, done <-chan error) {
	go func() {
		if err := <-done; err == nil {
			_ = syscall.Flock(fd, syscall.LOCK_UN)
		}
		f.Close()
	}()
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
