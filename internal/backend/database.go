package backend

import (
	"context"
	"errors"

	"github.com/LysCongThanh/worktree-pg/internal/errs"
	"github.com/LysCongThanh/worktree-pg/internal/names"
	"github.com/LysCongThanh/worktree-pg/internal/pg"
	"github.com/LysCongThanh/worktree-pg/internal/registry"
)

// Actions reported in Endpoint.Actions (empty on an idempotent re-run, S11).
const (
	ActCreateRole     = "create_role"
	ActCreateDatabase = "create_database"
	ActReassignOwned  = "reassign_owned"
)

// CodeOrphan marks a partial teardown: an object could not be dropped (exit 1).
const CodeOrphan = "ORPHANED_OBJECTS"

// Database clones one database (+ role) per worktree from the template on a
// shared cluster (spec §5 "Clone" and "Teardown").
type Database struct {
	Admin     *pg.Admin
	ClusterID string
	Host      string // host written to endpoints / DSNs
	Port      int
}

var _ Backend = (*Database)(nil)

// Ensure creates or verifies the role and database. On error the returned
// Endpoint still lists the actions done so far, so the caller can roll back only
// what this run created (invariant S4).
func (d *Database) Ensure(ctx context.Context, s Spec) (Endpoint, error) {
	ep := Endpoint{ClusterID: d.ClusterID, Host: d.Host, Port: d.Port, DB: s.DB, Role: s.Role, Password: s.Password, Actions: []string{}}
	perWT := s.RolesMode == "per_worktree"
	if perWT {
		created, err := d.ensureRole(ctx, s)
		if created {
			ep.Actions = append(ep.Actions, ActCreateRole)
		}
		if err != nil {
			return ep, err
		}
	}
	created, err := d.ensureDB(ctx, s, perWT)
	if created {
		ep.Actions = append(ep.Actions, ActCreateDatabase)
	}
	if err != nil {
		return ep, err
	}
	c, err := d.Admin.OnDatabase(ctx, s.DB)
	if err != nil {
		return ep, err
	}
	defer c.Close(context.WithoutCancel(ctx))
	if perWT {
		moved, err := c.HandOver(ctx, d.Admin, s.OwnerRole, s.Role)
		if moved {
			ep.Actions = append(ep.Actions, ActReassignOwned)
		}
		if err != nil {
			return ep, err
		}
	}
	roles := append([]string{s.Role}, s.ConnectRoles...)
	return ep, d.Admin.SetupClone(ctx, s.DB, s.RevokePublicConnect, roles)
}

// ensureRole creates the worktree role (marked) or re-applies the password of
// ours; a foreign role with that name is refused.
func (d *Database) ensureRole(ctx context.Context, s Spec) (created bool, err error) {
	r, err := d.Admin.Role(ctx, s.Role)
	if err != nil {
		return false, err
	}
	switch {
	case r == nil:
		m := names.Marker{Kind: names.KindRole, Key: s.Key, Path: s.Path, Host: s.Marker.Host}
		if err := d.Admin.CreateMarkedRole(ctx, s.Role, s.Password, m.String()); err != nil {
			return false, pg.Wrap(err, "create role %s", s.Role)
		}
		created = true
	case !names.Owns(r.Comment, names.KindRole, s.Key):
		return false, foreign("role", s.Role)
	default:
		if err := d.Admin.SetPassword(ctx, s.Role, s.Password); err != nil {
			return false, pg.Wrap(err, "set password of %s", s.Role)
		}
	}
	return created, pg.Wrap(d.Admin.GrantToSelf(ctx, s.Role), "grant %s to the admin", s.Role)
}

// ensureDB clones the template unless our database already exists.
func (d *Database) ensureDB(ctx context.Context, s Spec, perWT bool) (created bool, err error) {
	info, err := d.Admin.Database(ctx, s.DB)
	if err != nil {
		return false, err
	}
	if info != nil {
		return false, d.checkOurs(ctx, info, s.Key, s.Role, perWT)
	}
	owner := ""
	if perWT {
		owner = s.Role
	}
	err = d.Admin.CloneDatabase(ctx, s.DB, s.Template, owner, s.Strategy)
	if pg.IsState(err, pg.CodeDuplicateDatabase) { // a parallel run won: re-check ownership
		return d.ensureDB(ctx, s, perWT)
	}
	if errors.Is(err, pg.ErrTemplateGone) {
		return false, err
	}
	if err != nil {
		return false, pg.Wrap(err, "clone %s from %s", s.DB, s.Template)
	}
	m := names.Marker{Kind: names.KindClone, Key: s.Key, Path: s.Path, FP: s.Marker.FP, Host: s.Marker.Host}
	return true, pg.Wrap(d.Admin.CommentDatabase(ctx, s.DB, m.String()), "mark database %s", s.DB)
}

// checkOurs proves a database belongs to worktree key: its marker matches, or
// (crash between CREATE and COMMENT) it is owned by our marked role.
func (d *Database) checkOurs(ctx context.Context, db *pg.DBInfo, key, role string, perWT bool) error {
	if names.Owns(db.Comment, names.KindClone, key) {
		return nil
	}
	if db.Comment == "" && perWT && db.Owner == role {
		if r, err := d.Admin.Role(ctx, role); err != nil || (r != nil && names.Owns(r.Comment, names.KindRole, key)) {
			return err
		}
	}
	return foreign("database", db.Name)
}

func foreign(kind, name string) error {
	return errs.New(errs.Conflict, errs.CodeForeignResource, "%s %s exists but was not created by worktree-pg for this worktree", kind, name).
		WithHint("drop or rename it yourself; worktree-pg never touches objects it did not create")
}

// Destroy drops the worktree database WITH (FORCE) then its role, only when they
// are ours (S1). A role that still has dependents is reported as an orphan.
func (d *Database) Destroy(ctx context.Context, w *registry.Worktree) error {
	if w.DB != "" {
		info, err := d.Admin.Database(ctx, w.DB)
		if err != nil {
			return err
		}
		if info != nil {
			if err := d.checkOurs(ctx, info, w.Key, w.Role, true); err != nil {
				return err
			}
			if err := d.Admin.DropDatabase(ctx, w.DB, true); err != nil {
				return pg.Wrap(err, "drop database %s", w.DB)
			}
		}
	}
	if w.Role == "" {
		return nil
	}
	r, err := d.Admin.Role(ctx, w.Role)
	if err != nil || r == nil || !names.Owns(r.Comment, names.KindRole, w.Key) {
		return err // absent, or not ours (shared mode uses the admin role): leave it
	}
	err = d.Admin.DropRole(ctx, w.Role)
	if pg.IsState(err, pg.CodeDependentObjects) {
		return errs.Wrap(errs.Internal, CodeOrphan, err, "role %s still owns objects elsewhere; left in place", w.Role).
			WithHint("drop the dependent objects, then run `worktree-pg gc --yes`")
	}
	return pg.Wrap(err, "drop role %s", w.Role)
}

// Inspect reports the live state of the worktree's database and role.
func (d *Database) Inspect(ctx context.Context, w *registry.Worktree) (Health, error) {
	var h Health
	db, err := d.Admin.Database(ctx, w.DB)
	if err != nil {
		return h, err
	}
	r, err := d.Admin.Role(ctx, w.Role)
	if err != nil {
		return h, err
	}
	h.DBExists, h.RoleExists = db != nil, r != nil
	h.MarkerOK = db != nil && names.Owns(db.Comment, names.KindClone, w.Key) &&
		(r == nil || r.Superuser || names.Owns(r.Comment, names.KindRole, w.Key) || r.Name == d.Admin.Info.User)
	if db != nil {
		h.Sessions, err = d.Admin.Sessions(ctx, w.DB)
	}
	return h, err
}
