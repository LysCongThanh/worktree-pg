//go:build integration

// Package pgtest provides integration-test fixtures against a real PostgreSQL:
// the superuser DSN from WTPG_TEST_ADMIN_DSN, freshly created NOSUPERUSER admin
// roles (spec §8: every test runs as superuser AND as a CREATEDB CREATEROLE
// NOSUPERUSER admin) and unique app names whose objects are dropped on cleanup.
package pgtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/LysCongThanh/worktree-pg/internal/pg"
)

// EnvDSN names the variable holding the superuser DSN of the test cluster.
const EnvDSN = "WTPG_TEST_ADMIN_DSN"

// Admin is one admin identity a test runs as.
type Admin struct {
	Name string // subtest name: superuser | nosuper | shared
	DSN  string
}

// SuperDSN returns the superuser DSN or skips the test when it is not set.
func SuperDSN(t testing.TB) string {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv(EnvDSN))
	if dsn == "" {
		t.Skip(EnvDSN + " not set; run `make itest`")
	}
	return dsn
}

// App returns a unique app name (<= 20 bytes) and registers a cleanup that drops
// every database and role whose name starts with "<app>_" (admins last).
func App(t testing.TB) string {
	t.Helper()
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	app := "it" + hex.EncodeToString(b)
	super := SuperDSN(t)
	t.Cleanup(func() { cleanup(t, super, app) })
	return app
}

// Admins returns the superuser and a NOSUPERUSER CREATEDB CREATEROLE admin
// created for app (dropped by App's cleanup).
func Admins(t testing.TB, app string) []Admin {
	t.Helper()
	return []Admin{{"superuser", SuperDSN(t)}, {"nosuper", NewAdmin(t, app, "nosuper", "CREATEDB CREATEROLE")}}
}

// NewAdmin creates the LOGIN role <app>_adm_<label> NOSUPERUSER with attrs and
// returns its DSN on the test cluster.
func NewAdmin(t testing.TB, app, label, attrs string) string {
	t.Helper()
	ctx := context.Background()
	super := SuperDSN(t)
	a, err := pg.Connect(ctx, super)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close(ctx)
	name, pw := app+"_adm_"+label, "pw_"+app
	if err := a.Exec(ctx, "CREATE ROLE "+pg.Ident(name)+" LOGIN NOSUPERUSER "+attrs+" PASSWORD "+pg.Literal(pw)); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgx.ParseConfig(super)
	if err != nil {
		t.Fatal(err)
	}
	return pg.URL(pg.Cred{Host: cfg.Host, Port: int(cfg.Port), User: name, Password: pw, Database: cfg.Database, Params: "sslmode=disable"})
}

// Connect opens an admin connection or fails the test.
func Connect(t testing.TB, dsn string) *pg.Admin {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	a, err := pg.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close(context.Background()) })
	return a
}

// cleanup drops the app's databases (un-templating them first), then worktree
// roles, then admin roles, as superuser. Failures are logged, not fatal.
func cleanup(t testing.TB, super, app string) {
	ctx := context.Background()
	a, err := pg.Connect(ctx, super)
	if err != nil {
		t.Logf("cleanup connect: %v", err)
		return
	}
	defer a.Close(ctx)
	like := strings.ReplaceAll(app, "_", `\_`) + `\_%`
	dbs := query(ctx, t, a, "select datname::text from pg_database where datname like $1", like)
	for _, d := range dbs {
		_ = a.SetTemplate(ctx, d, false)
		if err := a.DropDatabase(ctx, d, true); err != nil {
			t.Logf("cleanup drop database %s: %v", d, err)
		}
	}
	roles := query(ctx, t, a, `select rolname::text from pg_roles where rolname like $1
		order by (rolname like '%\_adm\_%'), rolname`, like)
	for _, r := range roles {
		if err := a.DropRole(ctx, r); err != nil {
			t.Logf("cleanup drop role %s: %v", r, err)
		}
	}
}

func query(ctx context.Context, t testing.TB, a *pg.Admin, q string, args ...any) []string {
	rows, err := a.Conn().Query(ctx, q, args...)
	if err != nil {
		t.Logf("cleanup query: %v", err)
		return nil
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Logf("cleanup rows: %v", err)
	}
	return out
}
