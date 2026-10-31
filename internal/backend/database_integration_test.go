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
	"github.com/LysCongThanh/worktree-pg/internal/pg"
	"github.com/LysCongThanh/worktree-pg/internal/pg/pgtest"
	"github.com/LysCongThanh/worktree-pg/internal/registry"
	"github.com/LysCongThanh/worktree-pg/internal/template"
)

// fixture is one app with a published template on the test cluster.
type fixture struct {
	app    string
	admin  *pg.Admin
	db     *Database
	tpl    string
	fp     string
	reader string // cluster role granted SELECT through default privileges
}

// newFixture builds a template whose migration sets default privileges for a
// cluster-wide reader role, as admin dsn.
func newFixture(t *testing.T, app, dsn string) *fixture {
	t.Helper()
	ctx := context.Background()
	a := pgtest.Connect(t, dsn)
	reader := app + "_reader"
	if err := a.Exec(ctx, "CREATE ROLE "+pg.Ident(reader)+" NOLOGIN"); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	sql := `create table accounts(id serial primary key, tenant text not null);
create view v_accounts as select * from accounts;
create function f_one() returns int language sql as 'select 1';
create type mood as enum ('ok','bad');
alter table accounts enable row level security;
create policy tenant_iso on accounts using (tenant = current_setting('app.tenant', true));
alter default privileges grant select on tables to ` + reader + `;
alter default privileges revoke execute on functions from public;
`
	if err := os.WriteFile(filepath.Join(dir, "m.sql"), []byte(sql), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Name, cfg.Template.Inputs = app, []string{"m.sql"}
	cfg.Template.MigrateCmd = "psql -X -q -v ON_ERROR_STOP=1 -f m.sql"
	fp, err := template.Fingerprint(dir, cfg, a.Info, "")
	if err != nil {
		t.Fatal(err)
	}
	b := &template.Builder{Admin: a, Cfg: cfg, Dir: dir, FP: fp, Host: a.Host, Owner: names.OwnerRole(app),
		OwnerPwd: "ownerpw", StateDir: t.TempDir(), LogKey: "t", Getenv: os.Getenv}
	tpl, _, err := b.Ensure(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{app: app, admin: a, db: &Database{Admin: a, ClusterID: "c", Host: a.Host, Port: a.Port}, tpl: tpl, fp: fp, reader: reader}
}

// spec returns the clone spec of a worktree called slug.
func (f *fixture) spec(slug string) Spec {
	key := names.Key("/w/" + f.app + "/" + slug)
	db := names.Database(f.app, slug, key)
	return Spec{Key: key, Path: "/w/" + slug, App: f.app, Slug: slug, DB: db, Role: db, OwnerRole: names.OwnerRole(f.app),
		Template: f.tpl, RolesMode: "per_worktree", RevokePublicConnect: true, ConnectRoles: []string{f.reader},
		Password: "rolepw_" + slug, Marker: names.Marker{Kind: names.KindClone, Key: key, FP: f.fp}}
}

func wt(s Spec) *registry.Worktree { return &registry.Worktree{Key: s.Key, DB: s.DB, Role: s.Role} }

func eachAdmin(t *testing.T, fn func(t *testing.T, f *fixture)) {
	for _, kind := range []string{"superuser", "nosuper"} {
		t.Run(kind, func(t *testing.T) {
			app := pgtest.App(t)
			dsn := pgtest.Admins(t, app)[slices.Index([]string{"superuser", "nosuper"}, kind)].DSN
			fn(t, newFixture(t, app, dsn))
		})
	}
}

// TestInvariant_S11_CloneIdempotent: first Ensure creates role+db and hands
// objects over; the second reports no actions.
func TestInvariant_S11_CloneIdempotent(t *testing.T) {
	eachAdmin(t, func(t *testing.T, f *fixture) {
		ctx := context.Background()
		s := f.spec("a")
		ep, err := f.db.Ensure(ctx, s)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{ActCreateRole, ActCreateDatabase, ActReassignOwned}
		if !slices.Equal(ep.Actions, want) {
			t.Fatalf("actions %v, want %v", ep.Actions, want)
		}
		ep2, err := f.db.Ensure(ctx, s)
		if err != nil || len(ep2.Actions) != 0 {
			t.Fatalf("re-run: %v %v", ep2.Actions, err)
		}
		assertCloneUsable(t, f, s)
		h, err := f.db.Inspect(ctx, wt(s))
		if err != nil || !h.DBExists || !h.RoleExists || !h.MarkerOK {
			t.Fatalf("inspect %+v %v", h, err)
		}
		if err := f.db.Destroy(ctx, wt(s)); err != nil {
			t.Fatal(err)
		}
		if err := f.db.Destroy(ctx, wt(s)); err != nil {
			t.Fatalf("second destroy: %v", err)
		}
		if h, _ := f.db.Inspect(ctx, wt(s)); h.DBExists || h.RoleExists {
			t.Fatalf("leftovers after destroy: %+v", h)
		}
	})
}

// assertCloneUsable connects as the worktree role and checks ownership, migration
// rights, replayed default privileges and REVOKE CONNECT FROM PUBLIC.
func assertCloneUsable(t *testing.T, f *fixture, s Spec) {
	t.Helper()
	ctx := context.Background()
	r := pgtest.Connect(t, pg.URL(pg.Cred{Host: f.admin.Host, Port: f.admin.Port, User: s.Role, Password: s.Password, Database: s.DB, Params: "sslmode=disable"}))
	for _, q := range []string{"alter table accounts add column email text", "create table later(id int)",
		"drop policy tenant_iso on accounts", "create function f_two() returns int language sql as 'select 2'"} {
		if err := r.Exec(ctx, q); err != nil {
			t.Fatalf("as worktree role %q: %v", q, err)
		}
	}
	var leftover int
	var readerSel, ownerFn, publicFn, otherConn bool
	err := r.Conn().QueryRow(ctx, `select (select count(*) from pg_class c join pg_namespace n on n.oid = c.relnamespace
		where n.nspname = 'public' and pg_get_userbyid(c.relowner) <> current_user),
		has_table_privilege($1, 'later', 'SELECT'), has_function_privilege(current_user, 'f_one()', 'EXECUTE'),
		has_function_privilege('public', 'f_two()', 'EXECUTE'), has_database_privilege($2, current_database(), 'CONNECT')`,
		f.reader, names.OwnerRole(f.app)).Scan(&leftover, &readerSel, &ownerFn, &publicFn, &otherConn)
	if err != nil {
		t.Fatal(err)
	}
	if leftover != 0 || !readerSel || !ownerFn || publicFn || otherConn {
		t.Fatalf("leftover=%d readerSel=%v ownerFn=%v publicFn=%v otherConn=%v", leftover, readerSel, ownerFn, publicFn, otherConn)
	}
}

// TestInvariant_S1_ForeignObjectsUntouched: same-named unmarked database or role
// is refused by Ensure and Destroy, and survives.
func TestInvariant_S1_ForeignObjectsUntouched(t *testing.T) {
	eachAdmin(t, func(t *testing.T, f *fixture) {
		ctx := context.Background()
		s := f.spec("foreign")
		if err := f.admin.CreateDatabase(ctx, s.DB, "template0", "", ""); err != nil {
			t.Fatal(err)
		}
		if _, err := f.db.Ensure(ctx, s); errs.CodeOf(err) != errs.CodeForeignResource {
			t.Fatalf("Ensure over foreign db: %v", err)
		}
		if err := f.db.Destroy(ctx, wt(s)); errs.CodeOf(err) != errs.CodeForeignResource {
			t.Fatalf("Destroy foreign db: %v", err)
		}
		if d, _ := f.admin.Database(ctx, s.DB); d == nil {
			t.Fatal("foreign database dropped")
		}
		s2 := f.spec("frole")
		if err := f.admin.Exec(ctx, "CREATE ROLE "+pg.Ident(s2.Role)); err != nil {
			t.Fatal(err)
		}
		if _, err := f.db.Ensure(ctx, s2); errs.CodeOf(err) != errs.CodeForeignResource || errs.ExitCode(err) != 2 {
			t.Fatalf("Ensure over foreign role: %v", err)
		}
		if err := f.db.Destroy(ctx, wt(s2)); err != nil {
			t.Fatalf("Destroy must skip a foreign role: %v", err)
		}
		if r, _ := f.admin.Role(ctx, s2.Role); r == nil {
			t.Fatal("foreign role dropped")
		}
	})
}
