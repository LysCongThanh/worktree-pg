# Configuration: `.worktree-pg.toml`

worktree-pg reads `.worktree-pg.toml` from the root of the worktree it runs in, then the
optional `.worktree-pg.local.toml` next to it. Commit the first one and keep the second
one uncommitted. In the local overlay, scalars and arrays replace the base values, and
tables such as `[env.vars]` merge key by key. Unknown keys are an error (`CONFIG_INVALID`, exit 3).
`--config FILE` points at a config file somewhere else.

Working examples are in [`examples/`](../examples): Go + goose, Node + Medusa on a
managed docker cluster, and multiple DSNs with RLS roles.

## Top level

| key | default | meaning |
|---|---|---|
| `version` | required | always `1` |
| `name` | required | app name: `[a-z0-9_]`, at most 20 bytes. It prefixes every object name, e.g. `shop_wt_<slug>_<key8>` |

## `[cluster]`: where databases live

| key | default | meaning |
|---|---|---|
| `source` | `"external"` | `external` uses a server you already run. `docker` uses a container that worktree-pg manages: one per image, labelled, bound to loopback only |
| `admin_dsn_env` | `"WORKTREE_PG_ADMIN_DSN"` | env var holding the admin DSN. The DSN is **never** accepted as a CLI flag |
| `admin_dsn_file` | `""` | file holding the DSN. It must have mode `0600` |
| `image` | `""` | docker image, e.g. `postgres:16` (docker only) |
| `maintenance_db` | `"postgres"` | database the admin connects to |

The admin role needs `CREATEDB`. In `per_worktree` mode it also needs `CREATEROLE`.
Untrusted extensions need a superuser. `worktree-pg doctor` checks all of this.

## `[database]`: one clone per worktree

| key | default | meaning |
|---|---|---|
| `roles` | `"per_worktree"` | `per_worktree`: each clone gets its own login role, and template objects are reassigned to it. `shared`: the admin owns everything (no `CREATEROLE` needed) |
| `strategy` | `""` | `CREATE DATABASE ... STRATEGY`: `""`, `wal_log` or `file_copy`. Use `file_copy` above roughly 500 MB |
| `host` | `"127.0.0.1"` | host written into DSNs. Use `"localhost"` for stacks that insist on it |
| `revoke_public_connect` | `true` | `REVOKE CONNECT ... FROM PUBLIC` on each clone |

## `[template]`: the migrated template database

The template is rebuilt only when its **fingerprint** changes. The fingerprint covers the
PG major version, the image, the roles mode, the extensions, the command strings and a
sha256 of every file under `inputs`. File modification times are not part of it. A missing
input file is an error (exit 3).

| key | default | meaning |
|---|---|---|
| `inputs` | `[]` | files and directories hashed into the fingerprint, e.g. `["db/migrations"]` |
| `prepare_cmd` | `""` | runs before migrate, e.g. `npm ci` |
| `migrate_cmd` | required | builds the schema. It runs as the template owner role |
| `seed_cmd` | `""` | optional seed data |
| `extensions` | `[]` | created by the admin before migrate runs |
| `cluster_setup_cmd` | `""` | runs once per cluster per fingerprint, for cluster-wide roles (RLS) |
| `cluster_setup_inputs` | `[]` | files hashed for `cluster_setup_cmd` |
| `post_clone_cmd` | `""` | runs in each new clone |
| `keep` | `3` | number of templates `template gc` keeps |
| `timeout` | `"10m"` | limit for each user command, and the default `--wait` used by hooks |

User commands receive the DSN variables and `PG*` variables in their environment. Secrets
never appear in argv. Output goes to `<state>/logs/<key>-<ts>-<label>.log` (mode 0600).
When a command fails, `up` exits 5 and prints the log path.

## `[[dsn]]`: connection-string variables

```toml
[[dsn]]
var = "DATABASE_URL"   # env var name
role = "@owner"        # the worktree role, or a cluster-wide role name
password_env = ""      # env var with that role's password (cluster roles only)
format = "url"         # url | kv
params = "sslmode=disable"
host = ""              # overrides [database].host for this DSN
```

Repeat the block when you need several DSNs, for example an app role and a migrator role
under RLS.

## `[ports]`: sticky dev ports

| key | default | meaning |
|---|---|---|
| `ranges` | `["5600-5999"]` | must not overlap the kernel's ephemeral range |
| `reserved` | `[]` | ports never handed out |
| `[ports.vars]` | `{}` | `VAR = ""` uses the default ranges. `VAR = "7000-7099"` gives that variable its own range |

A port is chosen from a hash of the worktree key and the variable name, with linear
probing. The port is taken only if both `:p` and `host:p` can be bound. worktree-pg never
hands out ports below 1024, ports in the ephemeral range, ports already in the registry or
ports bound by docker (stopped containers included). Once a worktree has a port it keeps
it. If the port is busy, `up` warns `PORT_BUSY`; `up --reassign-ports` picks a new one.

## `[env]`: managed env-file blocks

```toml
[env]
files = [".env"]          # relative to the worktree; parent dir must exist
on_duplicate = "warn"     # warn | error when a key is also set outside the block
[env.vars]                # Go text/template
COMPOSE_PROJECT_NAME = "{{.App}}_{{.Slug}}"
APP_URL = 'http://127.0.0.1:{{Port "PORT"}}'
GOOSE_DBSTRING = '{{DSN "DATABASE_URL"}}'
```

The template fields are `.App`, `.Slug`, `.Key`, `.Branch` and `.Path`, and the functions
`Port "VAR"` and `DSN "VAR"`. worktree-pg writes only between its marker lines. Everything
else in the file is kept byte for byte, including CRLF line endings. See [safety.md](safety.md).

## `[hooks.claude]` and `[hooks.git]`

```toml
[hooks.claude]
worktree_dir = ".claude/worktrees/{{.Name}}"  # relative to the project root, or absolute
branch = "worktree-{{.Name}}"
base = "origin-default"      # origin-default (origin/HEAD, else HEAD) | head | <any ref>
copy_worktreeinclude = true  # copy ignored files listed in .worktreeinclude
on_up_failure = "fail"       # fail: roll back worktree + branch | keep: keep them, print the path
remove_force = true          # `git worktree remove --force` on WorktreeRemove

[hooks.git]
auto_up = true               # post-checkout provisions new worktrees
```

`{{.Name}}` is the name Claude Code passes in. It must match `^[A-Za-z0-9._-]+$` and must
not contain `..`. See [agents.md](agents.md).

## Environment variables

| var | meaning |
|---|---|
| `WORKTREE_PG_ADMIN_DSN` | default admin DSN source (see `admin_dsn_env`) |
| `WORKTREE_PG_STATE_DIR` | state directory. Otherwise `$XDG_STATE_HOME/worktree-pg`, then `~/.local/state/worktree-pg` |
| `WORKTREE_PG_IN_HOOK` | set by worktree-pg while it runs `git worktree add`. The post-checkout hook then does nothing |
| `WORKTREE_PG_DEBUG` | when non-empty, the git hook prints why it skipped |
| `CLAUDE_PROJECT_DIR` | set by Claude Code. Used as the project root by `claude-hook` |
