//go:build integration

package backend

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/LysCongThanh/worktree-pg/internal/config"
	"github.com/LysCongThanh/worktree-pg/internal/errs"
	"github.com/LysCongThanh/worktree-pg/internal/names"
	"github.com/LysCongThanh/worktree-pg/internal/pg/pgtest"
	"github.com/LysCongThanh/worktree-pg/internal/template"
)

// TestInvariant_S5_ReassignOnlyAfterGuard: when the owner role owns a database,
// the clone is refused with OWNER_GUARD (exit 2) and nothing is reassigned.
func TestInvariant_S5_ReassignOnlyAfterGuard(t *testing.T) {
	eachAdmin(t, func(t *testing.T, f *fixture) {
		ctx := context.Background()
		owner := names.OwnerRole(f.app)
		if err := f.admin.CreateDatabase(ctx, f.app+"_owned_by_owner", "template0", owner, ""); err != nil {
			t.Fatal(err)
		}
		s := f.spec("guard")
		_, err := f.db.Ensure(ctx, s)
		if errs.CodeOf(err) != errs.CodeOwnerGuard || errs.ExitCode(err) != 2 {
			t.Fatalf("want OWNER_GUARD, got %v", err)
		}
		c, err := f.admin.OnDatabase(ctx, s.DB)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close(ctx)
		if n, err := c.OwnedObjects(ctx, owner); err != nil || n == 0 {
			t.Fatalf("objects were reassigned despite the guard: n=%d err=%v", n, err)
		}
		if d, _ := f.admin.Database(ctx, f.app+"_owned_by_owner"); d == nil || d.Owner != owner {
			t.Fatal("guarded database changed owner")
		}
	})
}

// TestInvariant_S2_NoForeignSessionsTerminated: Destroy terminates sessions on our
// clone only; a session on another database survives.
func TestInvariant_S2_NoForeignSessionsTerminated(t *testing.T) {
	eachAdmin(t, func(t *testing.T, f *fixture) {
		ctx := context.Background()
		other := f.app + "_other"
		if err := f.admin.CreateDatabase(ctx, other, "template0", "", ""); err != nil {
			t.Fatal(err)
		}
		bystander, err := f.admin.OnDatabase(ctx, other)
		if err != nil {
			t.Fatal(err)
		}
		defer bystander.Close(ctx)
		s := f.spec("s2")
		if _, err := f.db.Ensure(ctx, s); err != nil {
			t.Fatal(err)
		}
		inClone, err := f.admin.OnDatabase(ctx, s.DB)
		if err != nil {
			t.Fatal(err)
		}
		defer inClone.Close(ctx)
		if err := f.db.Destroy(ctx, wt(s)); err != nil {
			t.Fatal(err)
		}
		if err := bystander.Exec(ctx, "select 1"); err != nil {
			t.Fatalf("bystander session was terminated: %v", err)
		}
	})
}

// TestCrashAfterCreateBeforeComment: an unmarked clone owned by our marked role
// (crash between CREATE and COMMENT) is adopted and finished on re-run.
func TestCrashAfterCreateBeforeComment(t *testing.T) {
	eachAdmin(t, func(t *testing.T, f *fixture) {
		ctx := context.Background()
		s := f.spec("crash")
		if _, err := f.db.Ensure(ctx, s); err != nil {
			t.Fatal(err)
		}
		if err := f.admin.DropDatabase(ctx, s.DB, true); err != nil {
			t.Fatal(err)
		}
		if err := f.admin.CloneDatabase(ctx, s.DB, s.Template, s.Role, ""); err != nil {
			t.Fatal(err)
		}
		ep, err := f.db.Ensure(ctx, s)
		if err != nil || !slices.Equal(ep.Actions, []string{ActReassignOwned}) {
			t.Fatalf("resume: %v %v", ep.Actions, err)
		}
		assertCloneUsable(t, f, s)
	})
}

// TestSharedRolesMode: a CREATEDB-only admin (no CREATEROLE) builds and clones
// with roles = "shared": no roles are created and the admin role survives Destroy.
func TestSharedRolesMode(t *testing.T) {
	app := pgtest.App(t)
	a := pgtest.Connect(t, pgtest.NewAdmin(t, app, "shared", "CREATEDB"))
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "m.sql"), []byte("create table t(id int);\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Name, cfg.Database.Roles, cfg.Template.Inputs = app, "shared", []string{"m.sql"}
	cfg.Template.MigrateCmd = "psql -X -q -v ON_ERROR_STOP=1 -f m.sql"
	fp, _ := template.Fingerprint(dir, cfg, a.Info, "")
	b := &template.Builder{Admin: a, Cfg: cfg, Dir: dir, FP: fp, Host: a.Host, StateDir: t.TempDir(), LogKey: "t", Getenv: os.Getenv}
	tpl, _, err := b.Ensure(ctx)
	if err != nil {
		t.Fatal(err)
	}
	key := names.Key("/w/shared")
	s := Spec{Key: key, Path: "/w/shared", App: app, DB: names.Database(app, "shared", key), Role: a.Info.User,
		OwnerRole: a.Info.User, Template: tpl, RolesMode: "shared", RevokePublicConnect: true}
	db := &Database{Admin: a, ClusterID: "c", Host: a.Host, Port: a.Port}
	ep, err := db.Ensure(ctx, s)
	if err != nil || !slices.Equal(ep.Actions, []string{ActCreateDatabase}) {
		t.Fatalf("shared Ensure: %v %v", ep.Actions, err)
	}
	if ep2, err := db.Ensure(ctx, s); err != nil || len(ep2.Actions) != 0 {
		t.Fatalf("shared re-run: %v %v", ep2.Actions, err)
	}
	if err := db.Destroy(ctx, wt(s)); err != nil {
		t.Fatal(err)
	}
	if r, _ := a.Role(ctx, a.Info.User); r == nil {
		t.Fatal("admin role dropped")
	}
	if d, _ := a.Database(ctx, s.DB); d != nil {
		t.Fatal("shared clone not dropped")
	}
}
