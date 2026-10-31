//go:build e2e

package e2e

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// admin is one admin identity the DB-backed e2e scenarios run as.
type admin struct{ name, dsn string }

// superDSN returns the superuser DSN of the test cluster or skips the test.
func superDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("WTPG_TEST_ADMIN_DSN")
	if dsn == "" {
		t.Skip("WTPG_TEST_ADMIN_DSN not set")
	}
	return dsn
}

// newApp returns a unique app name and registers a superuser cleanup that drops
// every database and role whose name starts with "<app>_" (spec §8 fixtures).
func newApp(t *testing.T) string {
	t.Helper()
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	app := "e2" + hex.EncodeToString(b)
	t.Cleanup(func() { dropApp(t, app) })
	return app
}

// admins returns the superuser and a CREATEDB CREATEROLE NOSUPERUSER admin.
func admins(t *testing.T, app string) []admin {
	t.Helper()
	super := superDSN(t)
	name, pw := app+"_adm", "pw_"+app
	sqlExec(t, fmt.Sprintf("CREATE ROLE %s LOGIN NOSUPERUSER CREATEDB CREATEROLE PASSWORD '%s'", name, pw))
	cfg, err := pgx.ParseConfig(super)
	if err != nil {
		t.Fatal(err)
	}
	nosuper := fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable", name, pw, cfg.Host, cfg.Port, cfg.Database)
	return []admin{{"superuser", super}, {"nosuper", nosuper}}
}

// connect opens a superuser connection to database db ("" = the DSN's own).
func connect(t *testing.T, db string) *pgx.Conn {
	t.Helper()
	cfg, err := pgx.ParseConfig(superDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	if db != "" {
		cfg.Database = db
	}
	c, err := pgx.ConnectConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// sqlExec runs one statement as superuser on the maintenance database.
func sqlExec(t *testing.T, sql string) {
	t.Helper()
	c := connect(t, "")
	defer c.Close(context.Background())
	if _, err := c.Exec(context.Background(), sql); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

// names lists database (kind "db") or role (kind "role") names matching a LIKE
// pattern, sorted.
func names(t *testing.T, kind, like string) []string {
	t.Helper()
	q := "SELECT datname FROM pg_database WHERE datname LIKE $1 ORDER BY 1"
	if kind == "role" {
		q = "SELECT rolname FROM pg_roles WHERE rolname LIKE $1 ORDER BY 1"
	}
	c := connect(t, "")
	defer c.Close(context.Background())
	rows, err := c.Query(context.Background(), q, like)
	if err != nil {
		t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// wtNames lists per-worktree databases or roles of app (template owner excluded).
func wtNames(t *testing.T, kind, app string) []string {
	var out []string
	for _, n := range names(t, kind, app+`\_wt\_%`) {
		if n != app+"_wt_owner" {
			out = append(out, n)
		}
	}
	return out
}

// dropApp removes every database then role named "<app>_*" (test cleanup only).
func dropApp(t *testing.T, app string) {
	ctx := context.Background()
	c := connect(t, "")
	defer c.Close(ctx)
	for _, db := range names(t, "db", app+`\_%`) {
		id := pgx.Identifier{db}.Sanitize()
		_, _ = c.Exec(ctx, "ALTER DATABASE "+id+" IS_TEMPLATE false")
		if _, err := c.Exec(ctx, "DROP DATABASE IF EXISTS "+id+" WITH (FORCE)"); err != nil {
			t.Logf("cleanup %s: %v", db, err)
		}
	}
	roles := names(t, "role", app+`\_%`)
	for _, r := range roles { // admin last: it may own grants on the others
		if !strings.HasSuffix(r, "_adm") {
			dropRole(ctx, t, c, r)
		}
	}
	for _, r := range roles {
		if strings.HasSuffix(r, "_adm") {
			dropRole(ctx, t, c, r)
		}
	}
}

func dropRole(ctx context.Context, t *testing.T, c *pgx.Conn, r string) {
	id := pgx.Identifier{r}.Sanitize()
	_, _ = c.Exec(ctx, "DROP OWNED BY "+id)
	if _, err := c.Exec(ctx, "DROP ROLE IF EXISTS "+id); err != nil {
		t.Logf("cleanup role %s: %v", r, err)
	}
}

// heldSession opens a session on db and returns its backend pid; alive reports
// whether that exact backend still exists (invariant S2: never terminated).
func heldSession(t *testing.T, db string) (alive func() bool) {
	t.Helper()
	ctx := context.Background()
	c := connect(t, db)
	t.Cleanup(func() { _ = c.Close(ctx) })
	pid := c.PgConn().PID()
	return func() bool {
		if err := c.Ping(ctx); err != nil {
			return false
		}
		var n int
		probe := connect(t, "")
		defer probe.Close(ctx)
		_ = probe.QueryRow(ctx, "SELECT count(*) FROM pg_stat_activity WHERE pid = $1 AND datname = $2", int(pid), db).Scan(&n)
		return n == 1
	}
}

// projectConfig renders .worktree-pg.toml for app with migrateCmd.
func projectConfig(app, migrateCmd string) string {
	return fmt.Sprintf(`version = 1
name = %q
[template]
inputs = ["db"]
migrate_cmd = %q
[[dsn]]
var = "DATABASE_URL"
role = "@owner"
[ports]
ranges = ["15500-15699"]
[ports.vars]
PORT = ""
[env]
files = [".env"]
`, app, migrateCmd)
}

// migrateAll applies db/migrations/*.sql in order with psql (the e2e "migrator")
// and records each real migrate run in $WTPG_E2E_BUILD_LOG.
const migrateAll = `for f in db/migrations/*.sql; do psql -X -q -v ON_ERROR_STOP=1 -f "$f" || exit 1; done; echo build >> "$WTPG_E2E_BUILD_LOG"`

// sampleRepo returns fixture files: two migrations and a gitignored .env.
func sampleRepo(app, migrateCmd string) map[string]string {
	return map[string]string{
		".worktree-pg.toml":           projectConfig(app, migrateCmd),
		".gitignore":                  ".env\n.claude/\n",
		"db/migrations/001_items.sql": "CREATE TABLE items (id serial PRIMARY KEY, name text NOT NULL);\nINSERT INTO items(name) VALUES ('seed');\n",
		"db/migrations/002_orders.sql": "CREATE TABLE orders (id serial PRIMARY KEY, item_id int REFERENCES items(id));\n" +
			"CREATE INDEX orders_item_idx ON orders(item_id);\n",
	}
}
