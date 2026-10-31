package pg

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/LysCongThanh/worktree-pg/internal/errs"
)

// SQLSTATE codes the engine reacts to (spikes S3/S4).
const (
	CodeObjectInUse           = "55006" // source database is being accessed by other users
	CodeLockNotAvailable      = "55P03" // lock_timeout hit
	CodeDuplicateDatabase     = "42P04"
	CodeDuplicateObject       = "42710" // role already exists
	CodeUniqueViolation       = "23505" // catalog unique index: a concurrent CREATE ROLE committed first
	CodeDependentObjects      = "2BP01" // role cannot be dropped: objects depend on it
	CodeInvalidCatalogName    = "3D000" // database (or template) does not exist
	CodeInsufficientPrivilege = "42501"
	CodeUndefinedObject       = "42704"
)

// SQLState returns the SQLSTATE of a server error, or "".
func SQLState(err error) string {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return pe.Code
	}
	return ""
}

// IsState reports whether err is a server error with one of the given codes.
func IsState(err error, codes ...string) bool {
	s := SQLState(err)
	for _, c := range codes {
		if s != "" && s == c {
			return true
		}
	}
	return false
}

// Wrap classifies a database error: missing privileges become PG_PRIVILEGE
// (exit 4), cancellation stays Interrupted, connection failures become
// PG_UNREACHABLE and everything else is Internal. Already typed errors pass through.
func Wrap(err error, format string, a ...any) error {
	if err == nil {
		return nil
	}
	var typed *errs.Error
	if errors.As(err, &typed) {
		return err
	}
	if errors.Is(err, context.Canceled) {
		return errs.Wrap(errs.Interrupted, errs.CodeInterrupted, err, format, a...)
	}
	switch {
	case IsState(err, CodeInsufficientPrivilege):
		return errs.Wrap(errs.Prereq, errs.CodePGPrivilege, err, format, a...).
			WithHint("the admin role needs CREATEDB (and CREATEROLE for roles=\"per_worktree\")")
	case SQLState(err) == "" && pgconn.SafeToRetry(err):
		return errs.Wrap(errs.Prereq, errs.CodePGUnreachable, err, format, a...)
	}
	return errs.Wrap(errs.Internal, errs.CodeInternal, err, format, a...)
}
