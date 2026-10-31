# Using worktree-pg with coding agents

Parallel agents each work in their own git worktree. worktree-pg gives every worktree its
own database (a fast clone of one migrated template), its own sticky dev ports and its own
env block. Two agents therefore never run migrations against each other or fight over
`:3000`.

## Claude Code (WorktreeCreate / WorktreeRemove hooks)

```sh
worktree-pg hook install claude                  # .claude/settings.local.json (default)
worktree-pg hook install claude --scope project  # .claude/settings.json (commit it)
worktree-pg hook install claude --scope user     # ~/.claude/settings.json
worktree-pg hook install claude --print          # show the result, write nothing
```

The command merges these entries into the settings file. It keeps your other keys and
hooks, saves the previous file as `<file>.bak`, and running it twice changes nothing:

```json
{"hooks": {
  "WorktreeCreate": [{"hooks": [{"type": "command", "command": "worktree-pg claude-hook create", "timeout": 600}]}],
  "WorktreeRemove": [{"hooks": [{"type": "command", "command": "worktree-pg claude-hook remove", "timeout": 600}]}]
}}
```

`worktree-pg` must be on the `PATH` that Claude Code sees.

### What `claude-hook create` does

1. Reads the hook JSON from stdin (at most 1 MiB). It uses `name`, and ignores
   `session_id`, `cwd` and any other field. A name that does not match
   `^[A-Za-z0-9._-]+$`, or that contains `..`, is refused.
2. Takes the project root from `$CLAUDE_PROJECT_DIR`, then the payload `cwd`, then the
   process cwd. It renders `[hooks.claude].worktree_dir` and `.branch` with the name.
3. If the target is already a worktree on that branch, it reuses it. If the target exists
   but is anything else, it refuses with exit 2 and leaves the directory alone.
4. Runs `git worktree add -b <branch> <path> <base>` with `WORKTREE_PG_IN_HOOK=1`, so the git
   hook does not provision a second time.
5. Copies the files listed in `.worktreeinclude` that are also git-ignored, for example
   `.env.local`. It never overwrites a file and never copies symlinks.
6. Runs `worktree-pg up` for the new worktree.
7. Prints the worktree path as the **last line of stdout**. Everything else goes to stderr.

If `up` fails and `on_up_failure = "fail"` (the default), the hook rolls back what this run
created: the database and role, the worktree directory (`git worktree remove --force`) and
the branch (`git branch -D`). It then exits with the error's code, and Claude Code reports
that the worktree could not be created. With `"keep"`, the hook warns and prints the path
anyway.

A repository without `.worktree-pg.toml` still gets its worktree. The hook skips
provisioning, so you can install the hook at user scope.

### What `claude-hook remove` does

It reads `worktree_path`. If that directory is already gone, the hook tears down the
registry entry with that path and exits 0. Otherwise the path must be a linked worktree
that `git worktree list` shows, that is not the main worktree, and that is not locked. If
any of that fails, the hook exits 2 and changes nothing. Then it runs `down`,
`git worktree remove [--force]` (as set by `remove_force`) and `git branch -d` on the
branch the create hook made. `-d` only deletes merged branches, so unmerged work survives.

Claude Code does not fire WorktreeRemove when a `claude -p` run exits (spike S8). Run
`worktree-pg gc` from time to time, or `worktree-pg down --remove-worktree` inside a
leftover worktree.

## Plain git (post-checkout)

```sh
worktree-pg hook install git          # writes/extends $(git rev-parse --git-path hooks/post-checkout)
git worktree add ../shop-feat -b feat # the new worktree is provisioned automatically
```

The hook acts only on a brand-new checkout: the old ref is all zeros and the flag is `1`.
It also requires a config file in the repository, `[hooks.git].auto_up = true`, and
`WORKTREE_PG_IN_HOOK` unset. It **always exits 0**. When provisioning fails, the checkout is
kept, and the hook tells you to run `worktree-pg up` yourself.

If a foreign hook already exists, worktree-pg appends a marked block and saves the original
as `.bak`. If that hook ends in `exit`, move the block above the `exit`; worktree-pg warns
when this applies. When `core.hooksPath` points inside the work tree (husky, lefthook),
worktree-pg refuses to edit the committed files, prints the snippet and exits 2. Add the
snippet to your hook manager yourself.

## worktrunk and other worktree managers

[`examples/wt-pg`](../examples/wt-pg) is a small shell shim for tools that run a command
after creating, or before removing, a worktree:

```sh
wt-pg up   [path]   # provision (idempotent)
wt-pg down [path]   # tear down database, role and env block
```

Wire it into your tool's post-create and pre-remove hooks. Check your tool's documentation
for the hook names and the variable that holds the worktree path.

## Driving worktree-pg from an agent

Every command accepts `--json` and then prints **exactly one** document on stdout. Logs go
to stderr.

```json
{"schema":"worktree-pg/v1","ok":true,"command":"up","result":{...},"warnings":[{"code":"PORT_BUSY","message":"..."}]}
{"schema":"worktree-pg/v1","ok":false,"command":"up","warnings":[],"error":{"code":"MIGRATE_FAILED","exit":5,"message":"...","hint":"...","log":"/.../logs/...log"}}
```

| exit | meaning | typical codes |
|---|---|---|
| 0 | ok | |
| 1 | internal error or partial failure | `INTERNAL`, `NOT_IMPLEMENTED` |
| 2 | safety refusal | `FOREIGN_RESOURCE`, `ENV_TARGET_UNSAFE`, `ENV_BLOCK_CORRUPT`, `NOT_REGISTERED_WORKTREE`, `OWNER_GUARD`, `HOOKS_PATH_MANAGED` |
| 3 | config or usage | `CONFIG_INVALID`, `CONFIG_NOT_FOUND`, `PORTS_EXHAUSTED`, `USAGE` |
| 4 | prerequisite | `ADMIN_DSN_MISSING`, `PG_UNREACHABLE`, `PG_PRIVILEGE`, `DOCKER_UNAVAILABLE`, `DOCTOR_FAILED` |
| 5 | user command failed | `MIGRATE_FAILED`, `SEED_FAILED`, `SETUP_FAILED`, `POST_CLONE_FAILED` (see `error.log`) |
| 6 | busy (retry, or pass `--wait`) | `LEASE_HELD`, `LOCK_TIMEOUT`, `TEMPLATE_BUILD_TIMEOUT` |
| 7 | not provisioned | `NOT_PROVISIONED` (run `up`) |
| 130 | interrupted | `INTERRUPTED` |

Passwords appear in only one JSON document: `env --format json`. Agents usually do not need
them, because `worktree-pg run -- <cmd>` execs the command with the variables already set.
Without `--override`, variables you already exported take precedence.
