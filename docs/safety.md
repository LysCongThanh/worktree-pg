# Safety model

worktree-pg drops databases and roles and edits files that you own, so it has to be
conservative. The invariants below are pinned by tests named `TestInvariant_*` in the
package that enforces them (the [README](../README.md#safety-invariants) links each one).
Postgres tests run on 15-18 as a superuser and as a `NOSUPERUSER CREATEDB CREATEROLE` admin.

| id | invariant |
|---|---|
| S1 | worktree-pg drops or alters only objects that carry its marker (`COMMENT` / docker label `worktree-pg:v1 {...}`) **and** match a registry entry for the same worktree key. Anything else is `FOREIGN_RESOURCE` (exit 2) |
| S2 | It terminates sessions only on databases it created (the template build), never on yours |
| S3 | A template at rest has `IS_TEMPLATE true` and `ALLOW_CONNECTIONS false`. It is published only by an atomic `RENAME` from `<app>_tpl_<fp>_build` |
| S4 | No orphans: the intended names are recorded before `CREATE`, and a failed run drops what it created. A run killed before the worktree ever became ready is rolled back by the next `up` or by `gc --yes`; a worktree that was once ready is never rolled back (its database may hold your data) |
| S5 | `REASSIGN OWNED` runs only `BY <app>_wt_owner`, and only after a guard confirms that role owns no database or tablespace |
| S6 | Secrets never appear in argv, `state.json`, logs, or `up`/`status` JSON. Passwords are HMAC-derived from `<state>/secret.key` (0600) and are never stored. Role passwords reach Postgres only as a client-computed SCRAM-SHA-256 verifier, so the server log (which records a failed statement's text) never sees them |
| S7 | Env files are edited only inside the marker block. Symlinked targets, targets outside the worktree and corrupt blocks are refused. Writes are atomic and leave the file at mode 0600 |
| S8 | Ports are never ones already taken, reserved, ephemeral, below 1024 or docker-bound. worktree-pg never kills a process holding a port |
| S9 | worktree-pg never runs `rm -rf` on a computed path. Directories are removed only by `git worktree remove` on a listed, non-main, unlocked worktree, never with the double force that overrides a lock |
| S10 | The registry is updated under `flock` with tmp + fsync + rename. Network, docker and migrations never run while the lock is held |
| S11 | Re-running is idempotent: same names, ports and password, identical env bytes, `actions: []` |
| S12 | Managed containers bind the configured loopback address only |
| S13 | `gc` is a dry run unless `--yes`, and never touches locked or non-prunable worktrees. Unrecorded marked objects are collected only when they were created on this host and their worktree path is gone |

## Hooks

- **`claude-hook create`** rolls back only what the current run created. A reused worktree
  keeps its database, even when `up` fails.
- **`claude-hook remove`** and `down --remove-worktree` check, before touching anything,
  that the path is a listed, non-main, unlocked worktree (S9). The branch made by the create
  hook is deleted with `git branch -d`, so unmerged commits survive.
- **post-checkout** always exits 0, so a provisioning problem never breaks `git checkout` or
  `git worktree add`.
- **`hook install`** saves the old file as `.bak`. It edits only its own marked block in a
  git hook, or its own entries in Claude settings. It refuses to edit hooks under a
  `core.hooksPath` inside the work tree (husky/lefthook) and prints a snippet instead.

## Known trade-offs

- **Managed docker password (spike D6).** The container gets `POSTGRES_PASSWORD` through its
  environment, not argv, but `docker inspect` still shows it. Docker group membership is
  root-equivalent on Linux, so this exposes nothing new to anyone who can already run
  `docker inspect`. The container is bound to loopback only (S12).
- **Role passwords in server logs.** `CREATE ROLE` / `ALTER ROLE ... PASSWORD` send the
  derived password in the SQL text. If the server sets `log_statement = 'ddl'` or `'all'`,
  it lands in the server log. Keep statement logging off for DDL on dev clusters, or treat
  those logs as secret.
- **Untrusted extensions (spike D7).** These need a superuser admin. Admin-created extensions
  stay admin-owned after the ownership hand-over. `doctor` warns about this.
- **Newlines in env values.** These are written double-quoted, which dotenv readers accept.
  `source .env` in a shell may not read them the same way, so use `eval "$(worktree-pg env --format shell)"`.
- **Claude settings key order.** `hook install claude` re-encodes the settings JSON with
  sorted keys. The previous file is kept as `.bak`.

## Reporting

Please report security issues privately through GitHub security advisories on the
repository, not as public issues.
