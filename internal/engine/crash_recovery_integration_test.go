//go:build integration

package engine

import (
	"context"
	"os"
	"os/exec"
	"slices"
	"testing"
	"time"

	"github.com/LysCongThanh/worktree-pg/internal/errs"
	"github.com/LysCongThanh/worktree-pg/internal/pg/pgtest"
	"github.com/LysCongThanh/worktree-pg/internal/registry"
)

// leaseOf returns a lease held by the process cmd (alive or already reaped).
func leaseOf(cmd *exec.Cmd) *registry.Lease {
	host, _ := os.Hostname()
	return &registry.Lease{PID: cmd.Process.Pid, PIDStart: registry.ProcStart(cmd.Process.Pid), Host: host, Op: "up", Since: time.Now()}
}

// gc's rollback of a crashed up must not drop objects of a live `up` that took
// over the stale lease after gc read its snapshot (concurrent up/gc race). Once
// the lease is really stale, the intent objects are dropped and the entry failed.
func TestGCStaleRollbackRespectsLiveLease(t *testing.T) {
	app := pgtest.App(t)
	f := newRepo(t, app, adminDSNs(t, app)["nosuper"], "")
	ctx := context.Background()
	res := f.up(f.worktree("crash"))
	live := exec.Command("sleep", "30")
	dead := exec.Command("true")
	if err := live.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = live.Process.Kill(); _ = live.Wait() }()
	if err := dead.Run(); err != nil {
		t.Fatal(err)
	}
	var snap registry.Worktree
	setLease := func(l *registry.Lease) {
		err := f.e.Store.Update(ctx, func(st *registry.State) error {
			w := st.Worktrees[res.Worktree.Key]
			w.Status, w.Intent, w.Lease = registry.StatusProvisioning, &registry.Intent{DB: res.Database, Role: res.Role}, l
			snap = *w
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	setLease(leaseOf(dead))
	stale := snap // what gc saw: a crashed provisioning run
	setLease(leaseOf(live))
	super := pgtest.Connect(t, pgtest.SuperDSN(t))
	if err := f.e.rollbackStale(ctx, &stale); errs.CodeOf(err) != errs.CodeLeaseHeld {
		t.Fatalf("want LEASE_HELD while a live up holds the lease, got %v", err)
	}
	if d, _ := super.Database(ctx, res.Database); d == nil {
		t.Fatal("gc dropped the database of a live up")
	}
	setLease(leaseOf(dead))
	if err := f.e.rollbackStale(ctx, &snap); err != nil {
		t.Fatal(err)
	}
	if d, _ := super.Database(ctx, res.Database); d != nil {
		t.Fatal("stale intent database survived the rollback")
	}
	_ = f.e.Store.View(func(st *registry.State) error {
		if w := st.Worktrees[res.Worktree.Key]; w.Status != registry.StatusFailed || w.Lease != nil || w.Intent != nil {
			t.Fatalf("entry after rollback: %+v", w)
		}
		return nil
	})
}

// An up killed after CREATE DATABASE but before the worktree became ready (say,
// during post_clone_cmd) must not be taken as done by the next up: the stale
// intent is rolled back and the clone made again, so post_clone_cmd runs fully.
func TestUpAfterCrashedUpReprovisions(t *testing.T) {
	app := pgtest.App(t)
	f := newRepo(t, app, adminDSNs(t, app)["nosuper"], "")
	ctx := context.Background()
	wt := f.worktree("killed")
	first := f.up(wt)
	dead := exec.Command("true")
	if err := dead.Run(); err != nil {
		t.Fatal(err)
	}
	err := f.e.Store.Update(ctx, func(st *registry.State) error {
		w := st.Worktrees[first.Worktree.Key]
		w.Status, w.Intent, w.Lease = registry.StatusProvisioning, &registry.Intent{DB: first.Database, Role: first.Role}, leaseOf(dead)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	super := pgtest.Connect(t, pgtest.SuperDSN(t))
	c, err := super.OnDatabase(ctx, first.Database)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Exec(ctx, "create table half_done(x int)"); err != nil { // stands for a cut-short post_clone_cmd
		t.Fatal(err)
	}
	c.Close(ctx)
	again := f.up(wt)
	for _, want := range []string{"rollback_crashed_up", "create_database"} {
		if !slices.Contains(again.Actions, want) {
			t.Fatalf("actions %v lack %s", again.Actions, want)
		}
	}
	c, err = super.OnDatabase(ctx, again.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)
	var n int
	if err := c.Conn().QueryRow(ctx, "select count(*) from pg_class where relname = 'half_done'").Scan(&n); err != nil || n != 0 {
		t.Fatalf("half-provisioned clone was kept (n=%d err=%v)", n, err)
	}
	if third := f.up(wt); len(third.Actions) != 0 { // S11 still holds afterwards
		t.Fatalf("re-run actions %v", third.Actions)
	}
}
