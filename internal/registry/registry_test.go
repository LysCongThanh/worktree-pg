package registry

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/LysCongThanh/worktree-pg/internal/errs"
)

// TestMain lets the test binary act as a concurrent registry writer process.
func TestMain(m *testing.M) {
	if dir := os.Getenv("WTPG_REG_HELPER"); dir != "" {
		s, _ := Open(dir)
		s.LockTimeout = time.Minute // this test checks lost updates, not timeouts (slow fsync under load)
		for i := 0; i < 25; i++ {
			if err := s.Update(context.Background(), incr); err != nil {
				os.Exit(3)
			}
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// incr bumps a counter stored as a worktree port so lost updates are detectable.
func incr(st *State) error {
	w := st.Worktrees["counter"]
	if w == nil {
		w = &Worktree{Key: "counter", Ports: map[string]int{}}
		st.Worktrees["counter"] = w
	}
	w.Ports["N"]++
	return nil
}

func TestInvariant_S10_RegistryConcurrency(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.LockTimeout = time.Minute // 275 fsynced updates can exceed 10 s on a loaded disk
	var procs []*exec.Cmd
	for i := 0; i < 3; i++ { // subprocess writers (flock across processes)
		c := exec.Command(os.Args[0], "-test.run=^$")
		c.Env = append(os.Environ(), "WTPG_REG_HELPER="+dir)
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		procs = append(procs, c)
	}
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ { // goroutine writers (same process, separate fds)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				if err := s.Update(context.Background(), incr); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	for _, c := range procs {
		if err := c.Wait(); err != nil {
			t.Fatalf("helper failed: %v", err)
		}
	}
	_ = s.View(func(st *State) error {
		if n := st.Worktrees["counter"].Ports["N"]; n != 11*25 {
			t.Fatalf("lost updates: N=%d want %d", n, 11*25)
		}
		return nil
	})
	st, _ := os.Stat(filepath.Join(dir, StateFile))
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("state perm %o", st.Mode().Perm())
	}
	leftovers, _ := filepath.Glob(filepath.Join(dir, ".state-*"))
	if len(leftovers) != 0 {
		t.Fatalf("temp files left: %v", leftovers)
	}
}

func TestUpdateErrorDiscardsAndCorruptState(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	boom := errors.New("boom")
	if err := s.Update(context.Background(), func(st *State) error { incr(st); return boom }); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, StateFile)); !os.IsNotExist(err) {
		t.Fatal("state written despite fn error")
	}
	_ = os.WriteFile(filepath.Join(dir, StateFile), []byte("{"), 0o600)
	if err := s.View(func(*State) error { return nil }); errs.CodeOf(err) != errs.CodeStateCorrupt {
		t.Fatalf("corrupt state: %v", err)
	}
	_ = os.WriteFile(filepath.Join(dir, StateFile), []byte(`{"version":9}`), 0o600)
	if err := s.View(func(*State) error { return nil }); errs.CodeOf(err) != errs.CodeStateCorrupt {
		t.Fatalf("future version: %v", err)
	}
	_ = os.WriteFile(filepath.Join(dir, StateFile), []byte(`{"version":1}`), 0o600)
	_ = s.View(func(st *State) error {
		if st.Clusters == nil || st.Worktrees == nil {
			t.Fatal("nil maps")
		}
		return nil
	})
}

func TestStateDirResolution(t *testing.T) {
	env := map[string]string{}
	get := func(k string) string { return env[k] }
	home, _ := os.UserHomeDir()
	if d, _ := StateDir(get); d != filepath.Join(home, ".local/state/worktree-pg") {
		t.Fatal(d)
	}
	env["XDG_STATE_HOME"] = "/x"
	if d, _ := StateDir(get); d != "/x/worktree-pg" {
		t.Fatal(d)
	}
	env["WORKTREE_PG_STATE_DIR"] = "/y/s"
	if d, _ := StateDir(get); d != "/y/s" {
		t.Fatal(d)
	}
}

func TestLeases(t *testing.T) {
	now := time.Now()
	w := &Worktree{Path: "/w/a"}
	if stale, err := w.Acquire("up", now); err != nil || stale {
		t.Fatal(err, stale)
	}
	if _, err := w.Acquire("up", now); err != nil {
		t.Fatal("re-acquire by self failed:", err)
	}
	// a live foreign process (our parent) holds the lease
	pp := os.Getppid()
	w.Lease = &Lease{PID: pp, PIDStart: ProcStart(pp), Host: hostname(), Op: "up", Since: now}
	if _, err := w.Acquire("down", now); errs.CodeOf(err) != errs.CodeLeaseHeld || errs.ExitCode(err) != 6 {
		t.Fatalf("want LEASE_HELD, got %v", err)
	}
	w.Release() // not ours: must stay
	if w.Lease == nil {
		t.Fatal("released foreign lease")
	}
	w.Lease.PIDStart = "1" // PID reuse: same pid, different start time
	if stale, err := w.Acquire("down", now); err != nil || !stale {
		t.Fatalf("pid-reuse lease not stale: %v", err)
	}
	w.Release()
	if w.Lease != nil {
		t.Fatal("own lease not released")
	}
	dead := &Lease{PID: 1 << 22, Host: hostname(), Since: now}
	other := &Lease{PID: pp, Host: "elsewhere", Since: now.Add(-3 * time.Hour)}
	if !dead.Stale(now) || !other.Stale(now) || (&Lease{PID: pp, Host: "elsewhere", Since: now}).Stale(now) {
		t.Fatal("stale detection")
	}
	var nilLease *Lease
	if !nilLease.Stale(now) {
		t.Fatal("nil lease should be stale")
	}
}

func TestParseStatStart(t *testing.T) {
	fields := ""
	for i := 3; i <= 25; i++ {
		fields += " " + strconv.Itoa(i)
	}
	if got := ParseStatStart("123 (we ird) proc)" + fields); got != "22" {
		t.Fatalf("got %q", got)
	}
	if ParseStatStart("garbage") != "" || ParseStatStart("1 (x) 3 4") != "" {
		t.Fatal("malformed stat accepted")
	}
	if ProcStart(os.Getpid()) == "" && fileExists("/proc/self/stat") {
		t.Fatal("ProcStart empty on linux")
	}
	st := newState()
	st.Worktrees["a"] = &Worktree{Ports: map[string]int{"PORT": 5600}}
	st.Clusters["c"] = &Cluster{ID: "c", Kind: "docker", Port: 5432}
	if tp := st.TakenPorts("b"); tp[5600] != "a:PORT" || tp[5432] == "" || len(st.TakenPorts("a")) != 1 {
		t.Fatal(fmt.Sprint(tp))
	}
}

func hostname() string { h, _ := os.Hostname(); return h }

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }
