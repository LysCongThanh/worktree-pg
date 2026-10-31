package pg

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/LysCongThanh/worktree-pg/internal/names"
)

// RoleInfo is the catalog view of one role.
type RoleInfo struct {
	Name      string
	Comment   string // shobj_description: holds the worktree-pg marker
	Superuser bool
}

const roleCols = `r.rolname::text, coalesce(shobj_description(r.oid, 'pg_authid'), ''), r.rolsuper`

// Role returns the role named name, or nil when it does not exist.
func (a *Admin) Role(ctx context.Context, name string) (*RoleInfo, error) {
	var r RoleInfo
	err := a.conn.QueryRow(ctx, "select "+roleCols+" from pg_roles r where r.rolname = $1", name).
		Scan(&r.Name, &r.Comment, &r.Superuser)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, Wrap(err, "look up role %s", name)
	}
	return &r, nil
}

// MarkedRoles lists every role carrying a worktree-pg marker.
func (a *Admin) MarkedRoles(ctx context.Context) ([]RoleInfo, error) {
	rows, err := a.conn.Query(ctx, "select "+roleCols+` from pg_roles r
		where shobj_description(r.oid, 'pg_authid') like $1 order by r.rolname`, names.MarkerPrefix+"%")
	if err != nil {
		return nil, Wrap(err, "list marked roles")
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (RoleInfo, error) {
		var r RoleInfo
		return r, row.Scan(&r.Name, &r.Comment, &r.Superuser)
	})
}

// CreateMarkedRole creates a LOGIN role with the given password and its marker
// COMMENT in one implicit transaction (two statements in one simple-protocol
// query). A crash can therefore never leave an unmarked role behind, which every
// later run would refuse as foreign (S1) and gc could never collect (S4). A
// parallel creator blocks on the name until this commits, so it sees the marker.
// The password is sent only as its SCRAM verifier (S6, see ScramVerifier).
func (a *Admin) CreateMarkedRole(ctx context.Context, name, password, marker string) error {
	sql, err := createRoleSQL(name, password, marker)
	if err != nil {
		return err
	}
	return a.Exec(ctx, sql)
}

// SetPassword re-applies the (deterministic) password of an existing role, sent
// as its SCRAM verifier (same bytes every run, S6/S11).
func (a *Admin) SetPassword(ctx context.Context, name, password string) error {
	sql, err := setPasswordSQL(name, password)
	if err != nil {
		return err
	}
	return a.Exec(ctx, sql)
}

// createRoleSQL renders CreateMarkedRole's statement; it never holds the plaintext.
func createRoleSQL(name, password, marker string) (string, error) {
	v, err := ScramVerifier(name, password)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("CREATE ROLE %s LOGIN PASSWORD %s; COMMENT ON ROLE %s IS %s",
		Ident(name), Literal(v), Ident(name), Literal(marker)), nil
}

// setPasswordSQL renders SetPassword's statement; it never holds the plaintext.
func setPasswordSQL(name, password string) (string, error) {
	v, err := ScramVerifier(name, password)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("ALTER ROLE %s PASSWORD %s", Ident(name), Literal(v)), nil
}

// GrantToSelf makes the admin a member of role so it can create databases owned
// by it and REASSIGN its objects. PG16+ non-superusers need WITH INHERIT TRUE,
// SET TRUE (spike D2); superusers need nothing.
func (a *Admin) GrantToSelf(ctx context.Context, role string) error {
	if a.Info.Superuser {
		return nil
	}
	sql := "GRANT " + Ident(role) + " TO CURRENT_USER"
	if a.Info.NeedsSetGrant() {
		sql += " WITH INHERIT TRUE, SET TRUE"
	}
	return a.Exec(ctx, sql)
}

// DropRole runs DROP ROLE IF EXISTS. 2BP01 (dependent objects) is returned as is.
func (a *Admin) DropRole(ctx context.Context, name string) error {
	return a.Exec(ctx, "DROP ROLE IF EXISTS "+Ident(name))
}
