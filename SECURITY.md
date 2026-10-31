# Security policy

worktree-pg creates and drops databases and roles with an admin connection, writes env
files containing passwords and runs your migrate commands. We take bugs in those areas
seriously.

## Supported versions

Only the latest release (and `main`) receive fixes while the project is pre-1.0.

## Reporting a vulnerability

Please **do not open a public issue.** Report it privately through
[GitHub security advisories](https://github.com/LysCongThanh/worktree-pg/security/advisories/new)
("Report a vulnerability"). Include the version (`worktree-pg version`), your OS, the
PostgreSQL version, and steps to reproduce. Remove passwords and DSNs from any output
you attach; `worktree-pg doctor --json` never prints them.

You can expect an acknowledgement within 7 days and a fix or a mitigation plan within
30 days for confirmed issues. We will credit you in the release notes unless you prefer
otherwise.

## What counts

Examples of issues we want to hear about:

- dropping, altering or terminating sessions on a database or role worktree-pg did not
  create (safety invariants S1, S2, S5 in [docs/safety.md](docs/safety.md));
- secrets in argv, `state.json`, logs or `up`/`status` JSON (S6);
- writing outside the managed env block, following symlinks, or files left readable by
  other users (S7);
- removing directories other than via `git worktree remove` on an eligible worktree (S9);
- a managed container published on a non-loopback address (S12).

Known trade-offs that are documented, not vulnerabilities: the managed container's
`POSTGRES_PASSWORD` is visible to `docker inspect` (docker access is root-equivalent), and
passwords sent by `CREATE ROLE ... PASSWORD` can reach the server log if you set
`log_statement = 'ddl'` or `'all'` on the server.
