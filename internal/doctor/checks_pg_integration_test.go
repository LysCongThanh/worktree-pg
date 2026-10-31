//go:build integration

package doctor

import (
	"context"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
)

// TestDoctorAdminChecksIntegration runs the PG checks against a real server
// (WTPG_TEST_ADMIN_DSN, a superuser) and against a temporary NOCREATEDB role.
func TestDoctorAdminChecksIntegration(t *testing.T) {
	dsn := os.Getenv("WTPG_TEST_ADMIN_DSN")
	if dsn == "" {
		t.Skip("WTPG_TEST_ADMIN_DSN not set")
	}
	ctx := context.Background()
	cfg := cfgExternal + "extensions = [\"pgcrypto\", \"wtpg_no_such_ext\"]\n"
	root := newRepo(t, cfg)
	run := func(d string) map[string]Check {
		getenv := func(k string) string {
			if k == "WORKTREE_PG_ADMIN_DSN" {
				return d
			}
			return ""
		}
		return byName(Run(ctx, Options{Cwd: root, StateDir: t.TempDir(), Getenv: getenv}))
	}
	m := run(dsn)
	for name, want := range map[string]string{
		"admin connection": OK, "privileges": OK, "extension pgcrypto": OK, "extension wtpg_no_such_ext": Fail,
	} {
		if m[name].Status != want {
			t.Errorf("superuser: %s = %+v, want %s", name, m[name], want)
		}
	}

	// a LOGIN role without CREATEDB must fail the privileges check
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	const role, pw = "wtpg_doctor_weak", "weak_pw_1"
	_, _ = admin.Exec(ctx, "DROP ROLE IF EXISTS "+role)
	if _, err := admin.Exec(ctx, "CREATE ROLE "+role+" LOGIN NOCREATEDB PASSWORD '"+pw+"'"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP ROLE IF EXISTS "+role) })
	u, _ := url.Parse(dsn)
	u.User = url.UserPassword(role, pw)
	m = run(u.String())
	if m["admin connection"].Status != OK || m["privileges"].Status != Fail || m["privileges"].Hint == "" {
		t.Fatalf("weak role: %+v / %+v", m["admin connection"], m["privileges"])
	}
}
