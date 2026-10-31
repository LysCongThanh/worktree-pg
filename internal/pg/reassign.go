package pg

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/LysCongThanh/worktree-pg/internal/errs"
)

// This file hands a fresh clone's objects from the template owner role
// (<app>_wt_owner) to the worktree role. All functions run on a connection to the
// clone database itself.

// OwnerGuard refuses to REASSIGN when the owner role owns any shared object
// (database or tablespace): REASSIGN OWNED would move those too (invariant S5).
func (a *Admin) OwnerGuard(ctx context.Context, owner string) error {
	var n int
	err := a.conn.QueryRow(ctx, `select (select count(*) from pg_database where datdba = r.oid)
		+ (select count(*) from pg_tablespace where spcowner = r.oid) from pg_roles r where r.rolname = $1`, owner).Scan(&n)
	if err != nil {
		return Wrap(err, "owner guard for %s", owner)
	}
	if n > 0 {
		return errs.New(errs.Conflict, errs.CodeOwnerGuard, "role %s owns %d database(s)/tablespace(s); refusing REASSIGN OWNED", owner, n).
			WithHint("transfer those objects to another role; %s must only own template contents", owner)
	}
	return nil
}

// OwnedObjects counts objects in the current database still owned by owner,
// ignoring pg_default_acl rows (REASSIGN never moves those, spike S1). Zero means
// the clone was already handed over, which makes re-runs idempotent (S11).
func (a *Admin) OwnedObjects(ctx context.Context, owner string) (int, error) {
	var n int
	err := a.conn.QueryRow(ctx, `select count(*) from pg_shdepend s join pg_roles r on r.oid = s.refobjid
		where r.rolname = $1 and s.deptype = 'o' and s.classid <> 'pg_default_acl'::regclass
		and s.dbid = (select oid from pg_database where datname = current_database())`, owner).Scan(&n)
	return n, Wrap(err, "count objects owned by %s", owner)
}

// Transfer runs, in one transaction: ALTER SCHEMA ... OWNER TO to for every schema
// owned by owner (spike D1: a NOSUPERUSER admin's REASSIGN otherwise fails with
// "permission denied for schema"), REASSIGN OWNED BY owner TO to, and the replay of
// owner's default privileges as to's. Callers run OwnerGuard first.
func (a *Admin) Transfer(ctx context.Context, owner, to string) error {
	tx, err := a.conn.Begin(ctx)
	if err != nil {
		return Wrap(err, "begin transfer")
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	schemas, err := collectStrings(ctx, tx, `select format('ALTER SCHEMA %I OWNER TO %I', nspname, $2::text)
		from pg_namespace where nspowner = (select oid from pg_roles where rolname = $1)`, owner, to)
	if err != nil {
		return Wrap(err, "list schemas owned by %s", owner)
	}
	stmts := append(schemas, fmt.Sprintf("REASSIGN OWNED BY %s TO %s", Ident(owner), Ident(to)))
	acls, err := collectStrings(ctx, tx, defaultACLReplay, owner, to)
	if err != nil {
		return Wrap(err, "read default privileges of %s", owner)
	}
	for _, s := range append(stmts, acls...) {
		if _, err := tx.Exec(ctx, s); err != nil {
			return Wrap(err, "hand over clone objects from %s to %s", owner, to)
		}
	}
	return Wrap(tx.Commit(ctx), "commit transfer")
}

// defaultACLReplay turns owner's pg_default_acl rows into ALTER DEFAULT PRIVILEGES
// FOR ROLE <to> statements via aclexplode (spike S1). The owner's own implicit
// entry is skipped. A global function/type row without a PUBLIC entry encodes a
// REVOKE from PUBLIC, which is replayed as such.
const defaultACLReplay = `
with d as (
  select defaclobjtype t, defaclnamespace ns, defaclacl acl,
    case when defaclnamespace = 0 then '' else ' IN SCHEMA ' || quote_ident(defaclnamespace::regnamespace::text) end sch,
    case defaclobjtype when 'r' then 'TABLES' when 'S' then 'SEQUENCES' when 'f' then 'FUNCTIONS'
      when 'T' then 'TYPES' when 'n' then 'SCHEMAS' when 'L' then 'LARGE OBJECTS' end obj
  from pg_default_acl where defaclrole = (select oid from pg_roles where rolname = $1))
select format('ALTER DEFAULT PRIVILEGES FOR ROLE %I%s GRANT %s ON %s TO %s%s', $2::text, d.sch, a.privilege_type, d.obj,
    case when a.grantee = 0 then 'PUBLIC' else quote_ident(pg_get_userbyid(a.grantee)) end,
    case when a.is_grantable then ' WITH GRANT OPTION' else '' end)
from d, aclexplode(d.acl) a
where d.obj is not null and a.grantee <> (select oid from pg_roles where rolname = $1)
union all
select format('ALTER DEFAULT PRIVILEGES FOR ROLE %I REVOKE %s ON %s FROM PUBLIC', $2::text,
    case d.t when 'f' then 'EXECUTE' else 'USAGE' end, d.obj)
from d where d.ns = 0 and d.t in ('f', 'T')
  and not exists (select 1 from aclexplode(d.acl) a where a.grantee = 0)`

// HandOver moves template objects from owner to the worktree role when any are
// left in this clone (fresh clone, or a crash mid-way): the admin joins owner,
// the guard runs, then Transfer. moved is false on an idempotent re-run (S11).
func (a *Admin) HandOver(ctx context.Context, admin *Admin, owner, to string) (moved bool, err error) {
	n, err := a.OwnedObjects(ctx, owner)
	if err != nil || n == 0 {
		return false, err
	}
	if err := admin.GrantToSelf(ctx, owner); err != nil {
		return false, Wrap(err, "grant %s to the admin", owner)
	}
	if err := a.OwnerGuard(ctx, owner); err != nil {
		return false, err
	}
	return true, a.Transfer(ctx, owner, to)
}

// collectStrings runs a query returning one text column.
func collectStrings(ctx context.Context, tx pgx.Tx, q string, args ...any) ([]string, error) {
	rows, err := tx.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// SetupClone connects the clone's access rules: REVOKE CONNECT FROM PUBLIC (when
// revoke is set) and GRANT CONNECT to each role. Idempotent; run on every up.
func (a *Admin) SetupClone(ctx context.Context, db string, revoke bool, roles []string) error {
	if revoke {
		if err := a.Exec(ctx, "REVOKE CONNECT ON DATABASE "+Ident(db)+" FROM PUBLIC"); err != nil {
			return Wrap(err, "revoke public connect on %s", db)
		}
	}
	for _, r := range roles {
		if err := a.Exec(ctx, "GRANT CONNECT ON DATABASE "+Ident(db)+" TO "+Ident(r)); err != nil {
			return Wrap(err, "grant connect on %s to %s", db, r)
		}
	}
	return nil
}
