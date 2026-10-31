// Package errs defines the typed error model shared by every worktree-pg layer.
//
// Each error carries a Kind (which maps to a stable process exit code), a stable
// machine-readable Code (e.g. "FOREIGN_RESOURCE") used in --json output, a human
// message and an optional Hint telling the user how to fix the problem.
package errs

import (
	"context"
	"errors"
	"fmt"
)

// Kind classifies an error and determines the CLI exit code.
type Kind int

// Error kinds. The zero value is Internal so an unclassified error never looks
// like a safety refusal or a success.
const (
	Internal       Kind = iota // exit 1: bug, partial failure, unexpected I/O error
	Conflict                   // exit 2: safety refusal (foreign resource, unsafe env target, ...)
	Config                     // exit 3: invalid configuration, ports exhausted, missing inputs
	Prereq                     // exit 4: missing admin DSN, PG unreachable, privileges, docker
	UserCmd                    // exit 5: a user-configured command (migrate/seed/...) failed
	Busy                       // exit 6: lease held, lock timeout, template build timeout
	NotFound                   // exit 7: worktree not provisioned
	NotImplemented             // exit 1: feature stub not yet implemented
	Interrupted                // exit 130: context cancelled / signal
)

// Stable error codes referenced by tests, docs and JSON consumers.
const (
	CodeInternal          = "INTERNAL"
	CodeNotImplemented    = "NOT_IMPLEMENTED"
	CodeInterrupted       = "INTERRUPTED"
	CodeForeignResource   = "FOREIGN_RESOURCE"
	CodeEnvTargetUnsafe   = "ENV_TARGET_UNSAFE"
	CodeEnvBlockCorrupt   = "ENV_BLOCK_CORRUPT"
	CodeNotRegistered     = "NOT_REGISTERED_WORKTREE"
	CodeOwnerGuard        = "OWNER_GUARD"
	CodeConfigInvalid     = "CONFIG_INVALID"
	CodeConfigNotFound    = "CONFIG_NOT_FOUND"
	CodeInputMissing      = "INPUT_MISSING"
	CodePortsExhausted    = "PORTS_EXHAUSTED"
	CodeAdminDSNMissing   = "ADMIN_DSN_MISSING"
	CodePGUnreachable     = "PG_UNREACHABLE"
	CodePGPrivilege       = "PG_PRIVILEGE"
	CodeDockerUnavailable = "DOCKER_UNAVAILABLE"
	CodeGitUnavailable    = "GIT_UNAVAILABLE"
	CodeNotGitRepo        = "NOT_A_GIT_REPO"
	CodeGitFailed         = "GIT_FAILED"
	CodeSecretKeyUnsafe   = "SECRET_KEY_UNSAFE"
	CodeMigrateFailed     = "MIGRATE_FAILED"
	CodePrepareFailed     = "PREPARE_FAILED"
	CodeSeedFailed        = "SEED_FAILED"
	CodeSetupFailed       = "SETUP_FAILED"
	CodePostCloneFailed   = "POST_CLONE_FAILED"
	CodeLeaseHeld         = "LEASE_HELD"
	CodeLockTimeout       = "LOCK_TIMEOUT"
	CodeTemplateTimeout   = "TEMPLATE_BUILD_TIMEOUT"
	CodeNotProvisioned    = "NOT_PROVISIONED"
	CodeStateCorrupt      = "STATE_CORRUPT"
)

// exitCodes maps each Kind to its process exit status.
var exitCodes = map[Kind]int{
	Internal: 1, Conflict: 2, Config: 3, Prereq: 4, UserCmd: 5,
	Busy: 6, NotFound: 7, NotImplemented: 1, Interrupted: 130,
}

// Exit returns the process exit status for the kind.
func (k Kind) Exit() int {
	if c, ok := exitCodes[k]; ok {
		return c
	}
	return 1
}

// Error is the concrete error type returned across package boundaries.
type Error struct {
	Kind    Kind
	Code    string
	Msg     string
	Hint    string
	LogPath string // path of the captured output of a failed user command, if any
	Err     error  // wrapped cause (may be nil)
}

// Error renders "msg: cause" so wrapped errors stay readable on stderr.
func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %v", e.Msg, e.Err)
	}
	return e.Msg
}

// Unwrap exposes the cause to errors.Is / errors.As.
func (e *Error) Unwrap() error { return e.Err }

// New builds an error of the given kind and stable code.
func New(k Kind, code, format string, args ...any) *Error {
	return &Error{Kind: k, Code: code, Msg: fmt.Sprintf(format, args...)}
}

// Wrap builds an error of the given kind that wraps cause.
func Wrap(k Kind, code string, cause error, format string, args ...any) *Error {
	return &Error{Kind: k, Code: code, Msg: fmt.Sprintf(format, args...), Err: cause}
}

// WithHint returns the error with a remediation hint attached.
func (e *Error) WithHint(format string, args ...any) *Error {
	e.Hint = fmt.Sprintf(format, args...)
	return e
}

// WithLog attaches the log path of a failed user command.
func (e *Error) WithLog(path string) *Error {
	e.LogPath = path
	return e
}

// As returns the *Error inside err, synthesising Interrupted for context
// cancellation and Internal for any other foreign error. It returns nil for nil.
func As(err error) *Error {
	if err == nil {
		return nil
	}
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	if errors.Is(err, context.Canceled) {
		return &Error{Kind: Interrupted, Code: CodeInterrupted, Msg: "interrupted", Err: err}
	}
	return &Error{Kind: Internal, Code: CodeInternal, Msg: "internal error", Err: err}
}

// ExitCode maps any error to the documented exit status (0 for nil).
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	return As(err).Kind.Exit()
}

// CodeOf returns the stable code of err ("" for nil).
func CodeOf(err error) string {
	if err == nil {
		return ""
	}
	return As(err).Code
}

// Is reports whether err carries the given stable code.
func Is(err error, code string) bool { return err != nil && CodeOf(err) == code }
