package pg

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/LysCongThanh/worktree-pg/internal/names"
)

// DBInfo is the catalog view of one database.
type DBInfo struct {
	Name       string
	Owner      string
	Comment    string // shobj_description: holds the worktree-pg marker
	IsTemplate bool
	AllowConn  bool
}

// ErrTemplateGone means CREATE DATABASE ... TEMPLATE t failed because t was dropped
// concurrently (spike S3): the caller re-resolves the template and retries.
var ErrTemplateGone = errors.New("template database disappeared during clone")

// Clone retry policy for 55006 "source database is being accessed by other users".
// The server itself already waits 5 s before raising it (spike S4).
const (
	CloneAttempts  = 5
	cloneJitterMin = 200 * time.Millisecond
	cloneJitterMax = 4 * time.Second
)

const dbCols = `d.datname::text, pg_get_userbyid(d.datdba)::text,
	coalesce(shobj_description(d.oid, 'pg_database'), ''), d.datistemplate, d.datallowconn`

// Database returns the catalog entry for name, or nil when it does not exist.
func (a *Admin) Database(ctx context.Context, name string) (*DBInfo, error) {
	var d DBInfo
	err := a.conn.QueryRow(ctx, "select "+dbCols+" from pg_database d where d.datname = $1", name).
		Scan(&d.Name, &d.Owner, &d.Comment, &d.IsTemplate, &d.AllowConn)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, Wrap(err, "look up database %s", name)
	}
	return &d, nil
}

// MarkedDatabases lists every database carrying a worktree-pg marker.
func (a *Admin) MarkedDatabases(ctx context.Context) ([]DBInfo, error) {
	rows, err := a.conn.Query(ctx, "select "+dbCols+` from pg_database d
		where shobj_description(d.oid, 'pg_database') like $1 order by d.datname`, names.MarkerPrefix+"%")
	if err != nil {
		return nil, Wrap(err, "list marked databases")
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (DBInfo, error) {
		var d DBInfo
		return d, r.Scan(&d.Name, &d.Owner, &d.Comment, &d.IsTemplate, &d.AllowConn)
	})
}

// CreateDatabase runs CREATE DATABASE name TEMPLATE tpl [OWNER owner] [STRATEGY s].
func (a *Admin) CreateDatabase(ctx context.Context, name, tpl, owner, strategy string) error {
	sql := fmt.Sprintf("CREATE DATABASE %s TEMPLATE %s", Ident(name), Ident(tpl))
	if owner != "" {
		sql += " OWNER " + Ident(owner)
	}
	if strategy != "" {
		sql += " STRATEGY " + strings.ToUpper(strategy)
	}
	return a.Exec(ctx, sql)
}

// CloneDatabase is CreateDatabase with the clone retry policy: 55006 is retried
// CloneAttempts times with 0.2-4 s jitter; a vanished template yields
// ErrTemplateGone. 42P04 (duplicate) is returned unchanged for the caller to re-check.
func (a *Admin) CloneDatabase(ctx context.Context, name, tpl, owner, strategy string) error {
	var err error
	for i := 0; i < CloneAttempts; i++ {
		err = a.CreateDatabase(ctx, name, tpl, owner, strategy)
		switch {
		case err == nil:
			return nil
		case IsState(err, CodeInvalidCatalogName) && strings.Contains(err.Error(), "template"):
			return ErrTemplateGone
		case !IsState(err, CodeObjectInUse):
			return err
		}
		jitter := cloneJitterMin + rand.N(cloneJitterMax-cloneJitterMin)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(jitter):
		}
	}
	return err
}

// DropDatabase runs DROP DATABASE IF EXISTS, terminating sessions WITH (FORCE)
// when force is set. Callers must have proven ownership first (invariants S1, S2).
func (a *Admin) DropDatabase(ctx context.Context, name string, force bool) error {
	sql := "DROP DATABASE IF EXISTS " + Ident(name)
	if force {
		sql += " WITH (FORCE)"
	}
	return a.Exec(ctx, sql)
}

// CommentDatabase writes the marker (or any text) as the database COMMENT.
func (a *Admin) CommentDatabase(ctx context.Context, name, text string) error {
	return a.Exec(ctx, fmt.Sprintf("COMMENT ON DATABASE %s IS %s", Ident(name), Literal(text)))
}

// RenameDatabase renames from to to (template publication, invariant S3).
func (a *Admin) RenameDatabase(ctx context.Context, from, to string) error {
	return a.Exec(ctx, fmt.Sprintf("ALTER DATABASE %s RENAME TO %s", Ident(from), Ident(to)))
}

// PublishTemplate sets IS_TEMPLATE, writes the template marker and renames the
// frozen build database to its final name in one implicit transaction (three
// statements, one simple-protocol query). A crash can never leave a build that
// already carries the template marker, nor a published name without it (S3).
func (a *Admin) PublishTemplate(ctx context.Context, build, name, marker string) error {
	return a.Exec(ctx, fmt.Sprintf("ALTER DATABASE %s IS_TEMPLATE true; COMMENT ON DATABASE %s IS %s; ALTER DATABASE %s RENAME TO %s",
		Ident(build), Ident(build), Literal(marker), Ident(build), Ident(name)))
}

// SetTemplate toggles IS_TEMPLATE.
func (a *Admin) SetTemplate(ctx context.Context, name string, on bool) error {
	return a.Exec(ctx, fmt.Sprintf("ALTER DATABASE %s IS_TEMPLATE %t", Ident(name), on))
}

// SetAllowConnections toggles ALLOW_CONNECTIONS.
func (a *Admin) SetAllowConnections(ctx context.Context, name string, on bool) error {
	return a.Exec(ctx, fmt.Sprintf("ALTER DATABASE %s ALLOW_CONNECTIONS %t", Ident(name), on))
}

// TerminateSessions ends every other client session on name. Only ever called
// for databases worktree-pg created (template builds), invariant S2. Autovacuum
// workers are skipped: a NOSUPERUSER admin may not signal them, and RENAME /
// DROP DATABASE terminate them by themselves.
func (a *Admin) TerminateSessions(ctx context.Context, name string) error {
	_, err := a.conn.Exec(ctx, `select pg_terminate_backend(pid) from pg_stat_activity
		where datname = $1 and pid <> pg_backend_pid() and backend_type = 'client backend'`, name)
	return Wrap(err, "terminate sessions on %s", name)
}

// Sessions counts live sessions on name.
func (a *Admin) Sessions(ctx context.Context, name string) (int, error) {
	var n int
	err := a.conn.QueryRow(ctx, "select count(*) from pg_stat_activity where datname = $1", name).Scan(&n)
	return n, Wrap(err, "count sessions on %s", name)
}
