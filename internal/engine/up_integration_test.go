//go:build integration

package engine

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/LysCongThanh/worktree-pg/internal/errs"
	"github.com/LysCongThanh/worktree-pg/internal/pg/pgtest"
	"github.com/LysCongThanh/worktree-pg/internal/registry"
)

func errsHint(err error) string { return errs.As(err).Hint }

// TestInvariant_S11_UpIdempotent covers the full lifecycle as superuser and as a
// NOSUPERUSER admin: up provisions everything, a re-run changes nothing (same
// names, ports, password, env bytes, no actions), env matches the file, status is
// healthy, down removes everything, and a second down is a no-op.
func TestInvariant_S11_UpIdempotent(t *testing.T) {
	for _, kind := range []string{"superuser", "nosuper"} {
		t.Run(kind, func(t *testing.T) {
			app := pgtest.App(t)
			f := newRepo(t, app, adminDSNs(t, app)[kind], "")
			wt := f.worktree("feat-a")
			res := f.up(wt)
			for _, a := range []string{"cluster_setup", "build_template", "create_role", "create_database", "reassign_owned", "write_env"} {
				if !slices.Contains(res.Actions, a) {
					t.Fatalf("first up actions %v miss %s", res.Actions, a)
				}
			}
			if res.Database != app+"_wt_feat_a_"+res.Worktree.Key[:8] || res.Role != res.Database || len(res.Ports) != 2 {
				t.Fatalf("names/ports: %+v", res)
			}
			env1, _ := os.ReadFile(filepath.Join(wt, ".env"))
			again := f.up(wt)
			env2, _ := os.ReadFile(filepath.Join(wt, ".env"))
			if len(again.Actions) != 0 || !bytes.Equal(env1, env2) || !maps2Equal(res.Ports, again.Ports) {
				t.Fatalf("re-run not idempotent: actions=%v ports %v vs %v", again.Actions, res.Ports, again.Ports)
			}
			if st, _ := os.Stat(filepath.Join(wt, ".env")); st.Mode().Perm() != 0o600 {
				t.Fatalf(".env mode %o", st.Mode().Perm())
			}
			vars, err := f.e.Env(context.Background(), wt)
			if err != nil {
				t.Fatal(err)
			}
			got := map[string]string{}
			for _, v := range vars {
				got[v.Key] = v.Value
				if !strings.Contains(string(env1), v.Key+"=") {
					t.Fatalf(".env misses %s", v.Key)
				}
			}
			if !strings.Contains(got["READER_URL"], "user="+app+"_reader") || !strings.Contains(got["READER_URL"], "password=readerpw") ||
				got["GOOSE_DBSTRING"] != got["DATABASE_URL"] || got["NAME"] != app+"_feat_a" {
				t.Fatalf("vars %v", got)
			}
			c := pgtest.Connect(t, got["DATABASE_URL"]) // the worktree role can log in and migrate
			if err := c.Exec(context.Background(), "alter table items add column note text"); err != nil {
				t.Fatal(err)
			}
			c.Close(context.Background())
			sts, err := f.e.Status(context.Background(), StatusRequest{Path: wt})
			if err != nil || len(sts) != 1 || sts[0].Status != registry.StatusReady || sts[0].Health == nil ||
				!sts[0].Health.MarkerOK || sts[0].Template.Stale {
				t.Fatalf("status %+v %v", sts, err)
			}
			down, err := f.e.Down(context.Background(), DownRequest{Path: wt})
			if err != nil || !slices.Contains(down.Actions, "drop_database") || !slices.Contains(down.Actions, "drop_role") {
				t.Fatalf("down %+v %v", down, err)
			}
			if _, err := os.Stat(filepath.Join(wt, ".env")); !os.IsNotExist(err) {
				t.Fatal(".env created by worktree-pg should be deleted when empty")
			}
			if d2, err := f.e.Down(context.Background(), DownRequest{Path: wt}); err != nil || len(d2.Actions) != 0 {
				t.Fatalf("second down %+v %v", d2, err)
			}
			if _, err := f.e.Env(context.Background(), wt); errs.ExitCode(err) != 7 {
				t.Fatalf("env after down: %v", err)
			}
		})
	}
}

func maps2Equal(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// TestInvariant_S4_UpRollsBack: a failing post_clone_cmd drops what this run
// created; ports stay sticky; the entry is failed with no intent or lease.
func TestInvariant_S4_UpRollsBack(t *testing.T) {
	app := pgtest.App(t)
	f := newRepo(t, app, adminDSNs(t, app)["nosuper"], `post_clone_cmd = "exit 3"`)
	wt := f.worktree("broken")
	_, err := f.e.Up(context.Background(), UpRequest{Path: wt})
	if errs.CodeOf(err) != errs.CodePostCloneFailed || errs.ExitCode(err) != 5 {
		t.Fatalf("want POST_CLONE_FAILED, got %v", err)
	}
	a := pgtest.Connect(t, pgtest.SuperDSN(t))
	var w registry.Worktree
	_ = f.e.Store.View(func(st *registry.State) error {
		for _, x := range st.Worktrees {
			w = *x
		}
		return nil
	})
	if w.Status != registry.StatusFailed || w.Intent != nil || w.Lease != nil || len(w.Ports) != 2 || w.LastError == "" {
		t.Fatalf("registry after failure: %+v", w)
	}
	if d, _ := a.Database(context.Background(), w.DB); d != nil {
		t.Fatal("database created by the failed run survived")
	}
	if r, _ := a.Role(context.Background(), w.Role); r != nil {
		t.Fatal("role created by the failed run survived")
	}
}

// TestInvariant_S1_UpRefusesForeignDatabase: an unmarked database with the
// worktree's name is refused (exit 2) and left intact by the rollback.
func TestInvariant_S1_UpRefusesForeignDatabase(t *testing.T) {
	app := pgtest.App(t)
	f := newRepo(t, app, adminDSNs(t, app)["nosuper"], "")
	wt := f.worktree("taken")
	plan, err := f.e.Up(context.Background(), UpRequest{Path: wt, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	a := pgtest.Connect(t, pgtest.SuperDSN(t))
	if err := a.CreateDatabase(context.Background(), plan.Database, "template0", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.e.Up(context.Background(), UpRequest{Path: wt}); errs.CodeOf(err) != errs.CodeForeignResource || errs.ExitCode(err) != 2 {
		t.Fatalf("want FOREIGN_RESOURCE, got %v", err)
	}
	if d, _ := a.Database(context.Background(), plan.Database); d == nil {
		t.Fatal("foreign database dropped by rollback")
	}
}

// TestInvariant_S10_NoSlowWorkUnderLock: while a slow migration runs, the
// registry lock stays available to other processes.
func TestInvariant_S10_NoSlowWorkUnderLock(t *testing.T) {
	app := pgtest.App(t)
	f := newRepo(t, app, adminDSNs(t, app)["nosuper"], `prepare_cmd = "sleep 2"`)
	wt := f.worktree("slow")
	done := make(chan error, 1)
	go func() { _, err := f.e.Up(context.Background(), UpRequest{Path: wt}); done <- err }()
	time.Sleep(700 * time.Millisecond)
	st, _ := registry.Open(f.e.Opts.StateDir)
	st.LockTimeout = 500 * time.Millisecond
	if err := st.Update(context.Background(), func(*registry.State) error { return nil }); err != nil {
		t.Fatalf("registry locked during the template build: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
