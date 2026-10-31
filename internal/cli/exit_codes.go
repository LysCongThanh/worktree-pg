package cli

// Exit codes (spec §6). Every error carries an errs.Kind that maps to one of
// them via errs.ExitCode; this table is the human reference, also in docs/.
//
//	0   ok
//	1   internal error or partial failure (orphans left, NOT_IMPLEMENTED)
//	2   safety refusal: FOREIGN_RESOURCE ENV_TARGET_UNSAFE ENV_BLOCK_CORRUPT
//	    NOT_REGISTERED_WORKTREE OWNER_GUARD HOOKS_PATH_MANAGED
//	3   config / usage: CONFIG_INVALID CONFIG_NOT_FOUND PORTS_EXHAUSTED USAGE
//	4   prerequisite: ADMIN_DSN_MISSING PG_UNREACHABLE PG_PRIVILEGE
//	    DOCKER_UNAVAILABLE DOCTOR_FAILED
//	5   user command failed: MIGRATE/SEED/SETUP/POST_CLONE_FAILED (+ log path)
//	6   busy: LEASE_HELD LOCK_TIMEOUT TEMPLATE_BUILD_TIMEOUT
//	7   NOT_PROVISIONED
//	130 interrupted (SIGINT / SIGTERM)
const (
	exitOK = 0

	// CodeUsage marks an invalid command line (unknown command/flag, missing
	// argument). Kind errs.Config, exit 3.
	CodeUsage = "USAGE"
)
