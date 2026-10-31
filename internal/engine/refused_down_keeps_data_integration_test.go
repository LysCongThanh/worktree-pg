//go:build integration

package engine

import (
	"context"
	"os/exec"
	"slices"
	"testing"

	"github.com/LysCongThanh/worktree-pg/internal/pg/pgtest"
	"github.com/LysCongThanh/worktree-pg/internal/registry"
)

// A refused `down` (here: --remove-worktree on a locked worktree) must leave a
// ready worktree ready. Before the fix it became "failed", so the next `up`
// recorded a provisioning intent; if that up was killed, the following up (or
// gc --yes) took it for a crashed first provisioning and dropped the database
// holding the developer's data.
func TestRefusedDownKeepsReadyWorktreeData(t *testing.T) {
	app := pgtest.App(t)
	f := newRepo(t, app, adminDSNs(t, app)["nosuper"], "")
	ctx := context.Background()
	wt := f.worktree("keep")
	res := f.up(wt)
	super := pgtest.Connect(t, pgtest.SuperDSN(t))
	c, err := super.OnDatabase(ctx, res.Database)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Exec(ctx, "create table dev_data(x int)"); err != nil {
		t.Fatal(err)
	}
	c.Close(ctx)

	f.git(f.main, "worktree", "lock", wt)
	if _, err := f.e.Down(ctx, DownRequest{Path: wt, RemoveWorktree: true}); err == nil {
		t.Fatal("down --remove-worktree of a locked worktree must be refused")
	}
	f.git(f.main, "worktree", "unlock", wt)

	// What a re-run `up` killed with -9 leaves: its intent and a dead lease, the
	// status untouched (tryBeginUp keeps ready, demotes anything else).
	dead := exec.Command("true")
	if err := dead.Run(); err != nil {
		t.Fatal(err)
	}
	err = f.e.Store.Update(ctx, func(st *registry.State) error {
		w := st.Worktrees[res.Worktree.Key]
		if w.Status != registry.StatusReady {
			t.Errorf("refused down changed status to %q", w.Status)
			w.Status = registry.StatusProvisioning // as tryBeginUp would
		}
		w.Intent, w.Lease = &registry.Intent{DB: res.Database, Role: res.Role}, leaseOf(dead)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if rep, err := f.e.GC(ctx, GCRequest{Path: f.main}); err != nil {
		t.Fatal(err)
	} else {
		for _, it := range rep.Items {
			if it.Action == gcWouldDrop {
				t.Fatalf("gc would drop a ready worktree's objects: %+v", it)
			}
		}
	}
	again := f.up(wt)
	if slices.Contains(again.Actions, "rollback_crashed_up") || slices.Contains(again.Actions, "create_database") {
		t.Fatalf("re-up after a killed up recreated the database: %v", again.Actions)
	}
	c, err = super.OnDatabase(ctx, again.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)
	var n int
	if err := c.Conn().QueryRow(ctx, "select count(*) from pg_class where relname = 'dev_data'").Scan(&n); err != nil || n != 1 {
		t.Fatalf("developer data lost (n=%d err=%v)", n, err)
	}
}
