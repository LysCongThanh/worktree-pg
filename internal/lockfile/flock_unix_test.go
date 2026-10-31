//go:build unix

package lockfile

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/LysCongThanh/worktree-pg/internal/errs"
)

// TestMain doubles as a lock-holding child process when WTPG_LOCK_HELPER is set,
// so the test can prove the lock works across processes (not just goroutines).
func TestMain(m *testing.M) {
	if p := os.Getenv("WTPG_LOCK_HELPER"); p != "" {
		l, err := Acquire(context.Background(), p, true, time.Second)
		if err != nil {
			os.Exit(3)
		}
		os.Stdout.WriteString("locked\n")
		time.Sleep(2 * time.Second)
		_ = l.Release()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestExclusiveAcrossProcesses(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.lock")
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), "WTPG_LOCK_HELPER="+p)
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 7)
	if _, err := out.Read(buf); err != nil || string(buf) != "locked\n" {
		t.Fatalf("helper did not lock: %q %v", buf, err)
	}
	start := time.Now()
	_, err := Acquire(context.Background(), p, true, 200*time.Millisecond)
	if errs.CodeOf(err) != errs.CodeLockTimeout || errs.ExitCode(err) != 6 {
		t.Fatalf("want LOCK_TIMEOUT, got %v", err)
	}
	if time.Since(start) < 200*time.Millisecond {
		t.Fatal("returned before timeout")
	}
	l, err := Acquire(context.Background(), p, true, 5*time.Second) // helper exits after ~2s
	if err != nil {
		t.Fatalf("lock not released by dead process: %v", err)
	}
	_ = l.Release()
	_ = l.Release()
	_ = cmd.Wait()
}

func TestSharedLocksCoexistAndCancel(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "x.lock")
	a, err := Acquire(context.Background(), p, false, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Acquire(context.Background(), p, false, time.Second)
	if err != nil {
		t.Fatal("second shared lock failed:", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	if _, err := Acquire(ctx, p, true, 10*time.Second); errs.ExitCode(err) != 130 {
		t.Fatalf("want interrupted, got %v", err)
	}
	_ = a.Release()
	_ = b.Release()
	var nilLock *Lock
	if nilLock.Release() != nil {
		t.Fatal("nil release")
	}
	if st, _ := os.Stat(p); st.Mode().Perm() != 0o600 {
		t.Fatalf("lock perm %o", st.Mode().Perm())
	}
}

// A waiter that timed out must not keep the lock once its background flock wins,
// and a fresh waiter must get the lock promptly after release (blocking wait).
func TestAbandonedWaiterReleasesAndWaitersWakePromptly(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.lock")
	holder, err := Acquire(context.Background(), p, true, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(context.Background(), p, true, 50*time.Millisecond); errs.CodeOf(err) != errs.CodeLockTimeout {
		t.Fatalf("want LOCK_TIMEOUT, got %v", err)
	}
	got := make(chan time.Time, 1)
	go func() {
		l, err := Acquire(context.Background(), p, true, 5*time.Second)
		if err != nil {
			t.Error(err)
			got <- time.Time{}
			return
		}
		got <- time.Now()
		_ = l.Release()
	}()
	time.Sleep(100 * time.Millisecond)
	released := time.Now()
	_ = holder.Release()
	at := <-got
	if at.IsZero() {
		t.Fatal("waiter never got the lock: abandoned waiter kept it")
	}
	if d := at.Sub(released); d > time.Second {
		t.Fatalf("waiter woke %s after release", d)
	}
	l, err := Acquire(context.Background(), p, true, time.Second)
	if err != nil {
		t.Fatalf("lock leaked: %v", err)
	}
	_ = l.Release()
}
