//go:build integration

package template

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LysCongThanh/worktree-pg/internal/config"
	"github.com/LysCongThanh/worktree-pg/internal/errs"
	"github.com/LysCongThanh/worktree-pg/internal/names"
	"github.com/LysCongThanh/worktree-pg/internal/pg"
	"github.com/LysCongThanh/worktree-pg/internal/pg/pgtest"
)

// migrateSQL is a small schema touching every object kind REASSIGN must move.
const migrateSQL = `create table accounts(id serial primary key, tenant text not null, name text);
create view v_accounts as select * from accounts;
create function f_one() returns int language sql as 'select 1';
create type mood as enum ('ok','bad');
alter table accounts enable row level security;
create policy tenant_iso on accounts using (tenant = current_setting('app.tenant', true));
insert into accounts(tenant, name) values ('t1','a'), ('t2','b');
`

// newBuilder prepares a repo dir with migrate.sql and a Builder for app.
func newBuilder(t *testing.T, a *pg.Admin, app, migrate string) *Builder {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "migrate.sql"), []byte(migrate), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Name = app
	cfg.Template.Inputs = []string{"migrate.sql"}
	cfg.Template.MigrateCmd = "psql -X -q -v ON_ERROR_STOP=1 -f migrate.sql"
	cfg.Template.Extensions = []string{"pgcrypto"}
	fp, err := Fingerprint(dir, cfg, a.Info, "")
	if err != nil {
		t.Fatal(err)
	}
	return &Builder{Admin: a, Cfg: cfg, Dir: dir, FP: fp, Host: a.Host, Owner: names.OwnerRole(app),
		OwnerPwd: "ownerpw", StateDir: t.TempDir(), LogKey: "test", Getenv: os.Getenv}
}

func TestBuildPublishAndReuse(t *testing.T) {
	for _, kind := range []string{"superuser", "nosuper"} {
		t.Run(kind, func(t *testing.T) {
			app := pgtest.App(t)
			adm := pgtest.Admins(t, app)[map[string]int{"superuser": 0, "nosuper": 1}[kind]]
			a := pgtest.Connect(t, adm.DSN)
			b := newBuilder(t, a, app, migrateSQL)
			name, built, err := b.Ensure(context.Background())
			if err != nil || !built {
				t.Fatalf("Ensure: built=%v err=%v", built, err)
			}
			assertPublished(t, a, name, b.FP)
			if _, built, err := b.Ensure(context.Background()); err != nil || built {
				t.Fatalf("second Ensure rebuilt: built=%v err=%v", built, err)
			}
		})
	}
}

// TestInvariant_S3_TemplateAtRest: published only by rename, IS_TEMPLATE true and
// ALLOW_CONNECTIONS false, no build DB left, marker present.
func TestInvariant_S3_TemplateAtRest(t *testing.T) {
	app := pgtest.App(t)
	a := pgtest.Connect(t, pgtest.Admins(t, app)[1].DSN)
	b := newBuilder(t, a, app, migrateSQL)
	name, _, err := b.Ensure(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	assertPublished(t, a, name, b.FP)
}

func assertPublished(t *testing.T, a *pg.Admin, name, fp string) {
	t.Helper()
	ctx := context.Background()
	d, err := a.Database(ctx, name)
	if err != nil || d == nil {
		t.Fatalf("template %s missing: %v", name, err)
	}
	if !d.IsTemplate || d.AllowConn {
		t.Fatalf("template at rest: is_template=%v allow_conn=%v", d.IsTemplate, d.AllowConn)
	}
	if m, ok := names.ParseMarker(d.Comment); !ok || m.Kind != names.KindTemplate || m.FP != fp {
		t.Fatalf("template marker %q", d.Comment)
	}
	if d.Owner != a.Info.User {
		t.Fatalf("template owner %s, want admin %s", d.Owner, a.Info.User)
	}
	if bd, _ := a.Database(ctx, name+"_build"); bd != nil {
		t.Fatal("build database left behind")
	}
}

// TestConcurrentEnsureBuildsOnce: N parallel callers on a cold template → one build.
func TestConcurrentEnsureBuildsOnce(t *testing.T) {
	app := pgtest.App(t)
	dsn := pgtest.Admins(t, app)[1].DSN
	var builds atomic.Int32
	var wg sync.WaitGroup
	errc := make(chan error, 8)
	var bs []*Builder
	for i := 0; i < 8; i++ {
		b := newBuilder(t, pgtest.Connect(t, dsn), app, migrateSQL)
		b.Wait = 2 * time.Minute
		bs = append(bs, b)
	}
	for _, b := range bs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, built, err := b.Ensure(context.Background())
			if built {
				builds.Add(1)
			}
			errc <- err
		}()
	}
	wg.Wait()
	close(errc)
	for err := range errc {
		if err != nil {
			t.Fatal(err)
		}
	}
	if n := builds.Load(); n != 1 {
		t.Fatalf("%d builds, want exactly 1", n)
	}
}

// TestInvariant_S4_FailedMigrateLeavesNothing: a failing migrate_cmd is exit 5
// with a log path, and no build/template database or lock remains.
func TestInvariant_S4_FailedMigrateLeavesNothing(t *testing.T) {
	app := pgtest.App(t)
	a := pgtest.Connect(t, pgtest.Admins(t, app)[1].DSN)
	b := newBuilder(t, a, app, "select * from no_such_table;\n")
	_, _, err := b.Ensure(context.Background())
	if errs.CodeOf(err) != errs.CodeMigrateFailed || errs.ExitCode(err) != 5 || errs.As(err).LogPath == "" {
		t.Fatalf("want MIGRATE_FAILED exit 5 with log, got %v", err)
	}
	ctx := context.Background()
	for _, n := range []string{b.Name(), names.TemplateBuild(app, b.FP)} {
		if d, _ := a.Database(ctx, n); d != nil {
			t.Fatalf("%s left behind", n)
		}
	}
	if ok, _ := a.TryLock(ctx, pg.LockKey(b.Name())); !ok {
		t.Fatal("advisory lock still held")
	}
}
