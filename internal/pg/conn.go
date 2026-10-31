package pg

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/LysCongThanh/worktree-pg/internal/errs"
)

// DefaultConnectTimeout bounds a single connection attempt when the DSN sets none.
const DefaultConnectTimeout = 10 * time.Second

// Connect opens the admin connection described by dsn and probes the admin
// role's capabilities. The DSN is a secret: it never appears in errors.
func Connect(ctx context.Context, dsn string) (*Admin, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, errs.New(errs.Prereq, errs.CodeAdminDSNMissing, "admin DSN is not a valid connection string").
			WithHint("use postgres://user:password@host:port/postgres?sslmode=disable")
	}
	return ConnectConfig(ctx, cfg)
}

// ConnectConfig is Connect for an already parsed config. It forces
// standard_conforming_strings=on (Literal relies on it) and tags the session
// with application_name=worktree-pg so DBAs can spot it.
func ConnectConfig(ctx context.Context, cfg *pgx.ConnConfig) (*Admin, error) {
	cfg = cfg.Copy()
	if cfg.ConnectTimeout == 0 {
		cfg.ConnectTimeout = DefaultConnectTimeout
	}
	if cfg.RuntimeParams == nil {
		cfg.RuntimeParams = map[string]string{}
	}
	cfg.RuntimeParams["standard_conforming_strings"] = "on"
	if cfg.RuntimeParams["application_name"] == "" {
		cfg.RuntimeParams["application_name"] = "worktree-pg"
	}
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errs.Wrap(errs.Prereq, errs.CodePGUnreachable, err, "connect to %s:%d as %s", cfg.Host, cfg.Port, cfg.User).
			WithHint("check that the cluster is running and the admin DSN is correct")
	}
	a := &Admin{Host: cfg.Host, Port: int(cfg.Port), conn: conn, cfg: cfg}
	if err := a.probe(ctx); err != nil {
		_ = conn.Close(ctx)
		return nil, err
	}
	return a, nil
}

// probe fills Info from the server version and the admin role's attributes.
func (a *Admin) probe(ctx context.Context) error {
	const q = `select current_setting('server_version_num')::int, current_user::text,
		r.rolsuper, r.rolcreatedb, r.rolcreaterole from pg_roles r where r.rolname = current_user`
	i := &a.Info
	if err := a.conn.QueryRow(ctx, q).Scan(&i.VersionNum, &i.User, &i.Superuser, &i.CreateDB, &i.CreateRole); err != nil {
		return Wrap(err, "probe admin capabilities")
	}
	i.Major = i.VersionNum / 10000
	return nil
}

// Close closes the connection; safe on nil.
func (a *Admin) Close(ctx context.Context) {
	if a != nil && a.conn != nil {
		_ = a.conn.Close(ctx)
	}
}

// OnDatabase opens a second admin connection to another database of the same
// cluster (needed for per-database DDL such as ALTER SCHEMA or REASSIGN OWNED).
func (a *Admin) OnDatabase(ctx context.Context, db string) (*Admin, error) {
	cfg := a.cfg.Copy()
	cfg.Database = db
	return ConnectConfig(ctx, cfg)
}

// Cred returns the admin's own credentials for database db (shared roles mode and
// cluster_setup_cmd). The password is a secret.
func (a *Admin) Cred(db string) Cred {
	return Cred{Host: a.Host, Port: a.Port, User: a.cfg.User, Password: a.cfg.Password, Database: db, Params: "sslmode=" + sslMode(a.cfg)}
}

// sslMode maps the parsed TLS config back to a libpq sslmode for child commands.
func sslMode(cfg *pgx.ConnConfig) string {
	if cfg.TLSConfig == nil {
		return "disable"
	}
	if len(cfg.Fallbacks) > 0 && cfg.Fallbacks[0].TLSConfig == nil {
		return "prefer"
	}
	return "require"
}

// Exec runs one or more statements over the simple protocol (no implicit
// transaction around single statements, as CREATE/DROP DATABASE require).
func (a *Admin) Exec(ctx context.Context, sql string) error {
	_, err := a.conn.PgConn().Exec(ctx, sql).ReadAll()
	return err
}

// Ident quotes an identifier.
func Ident(s string) string { return pgx.Identifier{s}.Sanitize() }

// Literal quotes a string literal. Safe because every session forces
// standard_conforming_strings=on (backslashes are literal).
func Literal(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
