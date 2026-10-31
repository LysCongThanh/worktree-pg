//go:build integration

package pg_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/LysCongThanh/worktree-pg/internal/pg"
	"github.com/LysCongThanh/worktree-pg/internal/pg/pgtest"
)

// A role created from a client-side SCRAM verifier logs in with the plaintext
// password (real scram-sha-256 auth), and re-applying the password leaves the
// stored verifier byte-identical (S6 + S11).
func TestScramVerifierRoleLogsIn(t *testing.T) {
	for i, kind := range []string{"superuser", "nosuper"} {
		t.Run(kind, func(t *testing.T) {
			app := pgtest.App(t)
			a := pgtest.Connect(t, pgtest.Admins(t, app)[i].DSN)
			super := pgtest.Connect(t, pgtest.SuperDSN(t))
			ctx := context.Background()
			role, pw := app+"_wt_login", "Gen3rated-Pw_xyz"
			if err := a.CreateMarkedRole(ctx, role, pw, "worktree-pg:v1 {}"); err != nil {
				t.Fatal(err)
			}
			stored := func() string {
				var s string
				if err := super.Conn().QueryRow(ctx, "select rolpassword from pg_authid where rolname = $1", role).Scan(&s); err != nil {
					t.Fatal(err)
				}
				return s
			}
			want, _ := pg.ScramVerifier(role, pw)
			if got := stored(); got != want {
				t.Fatalf("stored %q, want our verifier %q", got, want)
			}
			if err := a.SetPassword(ctx, role, pw); err != nil {
				t.Fatal(err)
			}
			if got := stored(); got != want {
				t.Fatalf("re-applied password changed the verifier: %q", got)
			}
			cfg, err := pgx.ParseConfig(pgtest.SuperDSN(t))
			if err != nil {
				t.Fatal(err)
			}
			dsn := pg.URL(pg.Cred{Host: cfg.Host, Port: int(cfg.Port), User: role, Password: pw, Database: "postgres", Params: "sslmode=disable"})
			c, err := pgx.Connect(ctx, dsn)
			if err != nil {
				t.Fatalf("login with the plaintext password: %v", err)
			}
			_ = c.Close(ctx)
			bad := pg.URL(pg.Cred{Host: cfg.Host, Port: int(cfg.Port), User: role, Password: pw + "x", Database: "postgres", Params: "sslmode=disable"})
			if c, err := pgx.Connect(ctx, bad); err == nil {
				_ = c.Close(ctx)
				t.Fatal("wrong password accepted")
			}
		})
	}
}
