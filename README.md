# worktree-pg

**A migrated Postgres database per git worktree, in about 1.6 seconds. Built for parallel AI agents.**

`worktree-pg` gives every git worktree its own PostgreSQL database, cloned from a template
that was migrated once. Each worktree also gets its own login role, sticky dev ports and a
managed block in its `.env` file. Run five coding agents in five worktrees and none of them
migrates another one's database or fights it for `:3000`.

> Status: **v0.1 pre-release.** It works end to end on PostgreSQL 15-18 on Linux, and the
> whole test suite runs against real servers. macOS is best effort. Windows is not supported.
> Expect rough edges and please [report them](../../issues/new/choose).

## Why

Parallel agents need parallel databases. The usual workarounds each break something:

- **One shared dev database.** Agent A drops a column that agent B's branch still needs.
  Two test runs truncate each other's tables.
- **A container per worktree.** You pay for a whole server, its RAM and a 20-second start
  per worktree, and you still have to run every migration and seed into the empty database.
  Ports picked by hashing the branch name collide sooner than you think.
- **`createdb -T dev`.** Postgres refuses to copy a database that has other sessions
  (`55006 source database is being accessed by other users`), so tools in this space end up
  terminating the sessions on your dev database to make it work.

worktree-pg instead migrates a **dedicated template** once per set of migration files, and
then clones it with `CREATE DATABASE ... TEMPLATE` for every worktree. A clone is a file copy
inside the server, so it takes about the same time whether your migrations took 2 seconds or
2 minutes to run. Nobody's sessions are touched, because nobody connects to the template.

## 60-second quickstart

You need git, PostgreSQL 15+ that you can reach with a role that has `CREATEDB` and
`CREATEROLE` (or use `roles = "shared"` with only `CREATEDB`), and Go 1.25+ to install.

```sh
go install github.com/LysCongThanh/worktree-pg@latest

# 1. Tell it how to reach Postgres as an admin. Never passed as a flag; keep it in your shell profile.
export WORKTREE_PG_ADMIN_DSN=postgres://postgres:postgres@127.0.0.1:5432/postgres

# 2. Describe your project once (commit this file).
cat > .worktree-pg.toml <<'EOF'
version = 1
name = "shop"
[template]
inputs = ["db/migrations"]                     # hashed: the template is rebuilt only when these change
migrate_cmd = "goose -dir db/migrations up"    # runs with PG* env + DATABASE_URL pointing at the template
[[dsn]]
var = "DATABASE_URL"
[ports.vars]
PORT = ""
[env]
files = [".env"]
EOF
worktree-pg doctor                 # checks git, config, privileges, extensions

# 3. Provision automatically on every new worktree.
worktree-pg hook install git       # git post-checkout
worktree-pg hook install claude    # Claude Code WorktreeCreate / WorktreeRemove

git worktree add ../shop-feature -b feature
cat ../shop-feature/.env           # DATABASE_URL=postgres://shop_wt_shop_feature_.../  PORT=56xx
```

Day-to-day commands, all of them idempotent and all of them with `--json`:

```sh
worktree-pg up                     # provision this worktree (re-run: no changes, same ports)
worktree-pg run -- go test ./...   # run with this worktree's variables
eval "$(worktree-pg env --format shell)"
worktree-pg status --all           # every worktree on this machine
worktree-pg down                   # drop its database and role, strip the env block
worktree-pg gc --yes               # clean up after worktrees deleted with rm -rf (dry run without --yes)
worktree-pg template refresh       # build the template for the current inputs now (--force: rebuild it anyway)
```

Ready-made configs: [Go + goose](examples/go-goose/.worktree-pg.toml),
[Node + Medusa on a managed docker cluster](examples/node-medusa/.worktree-pg.toml),
[several DSNs with RLS roles](examples/multi-dsn-rls/.worktree-pg.toml).
Every key is documented in [docs/config.md](docs/config.md).

## How it works

```mermaid
flowchart LR
  subgraph once["once per fingerprint (migration files + commands + PG major)"]
    B["CREATE DATABASE shop_tpl_ab12cd34_build"] --> M["prepare / migrate / seed<br/>as role shop_wt_owner"]
    M --> F["ALLOW_CONNECTIONS false<br/>RENAME to shop_tpl_ab12cd34<br/>IS_TEMPLATE true"]
  end
  subgraph each["per worktree, about 1.6 s"]
    R["registry entry + lease<br/>sticky ports"] --> C["CREATE ROLE shop_wt_feat_1a2b3c4d<br/>CREATE DATABASE ... TEMPLATE shop_tpl_ab12cd34"]
    C --> H["hand objects to the worktree role<br/>(guarded REASSIGN OWNED)"]
    H --> E["write .env managed block (0600)"]
  end
  F --> C
  G["git worktree add<br/>Claude Code WorktreeCreate<br/>worktrunk / scripts"] --> R
```

- **Template.** The fingerprint covers the files under `inputs`, the command strings, the
  extensions, the roles mode and the server's major version (or the docker image). The
  template is built under an advisory lock, so 16 agents starting at once still trigger
  exactly one migrate run. It is published by an atomic rename and is never connectable at
  rest. Older templates are kept until `template gc`; clones never depend on them.
- **Clone.** Each worktree gets a database and a login role named
  `<app>_wt_<slug>_<key8>`. Its password is derived from a machine-local key, so it is never
  stored and never changes between runs. Objects created by the migrations are handed to
  the worktree role, so your app and your migration tool run as that role, as they would
  in production.
- **Ports.** Each variable in `[ports.vars]` gets a port that stays the same for that
  worktree. The port is chosen inside your ranges and skips ports that are reserved,
  privileged, in the OS ephemeral range, bound by any docker container (even stopped ones),
  or already in use.
- **State.** A small registry under `$XDG_STATE_HOME/worktree-pg` records what was created,
  under a file lock with atomic writes. Databases and roles also carry a `COMMENT` marker,
  so worktree-pg can tell its own objects from yours even if the registry is lost.

## Integrations

| Where | How |
|---|---|
| **Claude Code** | `worktree-pg hook install claude` replaces the worktree creation step: it runs `git worktree add`, copies `.worktreeinclude` files, provisions, and prints the path. `WorktreeRemove` tears it down. See [docs/agents.md](docs/agents.md) |
| **Plain git** | `worktree-pg hook install git` adds a post-checkout hook that provisions brand-new worktrees and never fails your checkout |
| **worktrunk and other worktree managers** | call [`examples/wt-pg`](examples/wt-pg) `up <path>` / `down <path>` from their post-create and pre-remove hooks |
| **Any agent or script** | `--json` prints exactly one JSON document, exit codes are stable (below), `env --format json` and `run --` hand over the variables |

Exit codes: `0` ok · `1` internal or partial · `2` safety refusal · `3` config ·
`4` missing prerequisite · `5` your migrate/seed/setup command failed (log path in the
error) · `6` busy, retry or pass `--wait` · `7` worktree not provisioned · `130` interrupted.

## Safety invariants

worktree-pg drops databases and edits files, so it is deliberately conservative. Each rule
below has a test named after it, and every Postgres test runs on 15, 16, 17 and 18, both as
a superuser and as a `CREATEDB CREATEROLE NOSUPERUSER` admin. Details: [docs/safety.md](docs/safety.md).

| | Invariant | Test |
|---|---|---|
| S1 | Only objects that carry our marker **and** match the registry are altered or dropped. A lookalike is refused with exit 2 | [backend](internal/backend/database_integration_test.go) `TestInvariant_S1_ForeignObjectsUntouched`, [engine](internal/engine/up_integration_test.go) `TestInvariant_S1_UpRefusesForeignDatabase`, [e2e](test/e2e/failure_e2e_test.go) `TestE2E_ForeignObjectsRefused` |
| S2 | Never terminates sessions on databases it did not create; your dev DB is never the template | [backend](internal/backend/invariants_integration_test.go) `TestInvariant_S2_NoForeignSessionsTerminated`, [e2e](test/e2e/scenario_e2e_test.go) `TestE2E_FullScenario` |
| S3 | A template at rest is `IS_TEMPLATE true`, `ALLOW_CONNECTIONS false`, published only by rename | [template](internal/template/build_integration_test.go) `TestInvariant_S3_TemplateAtRest` |
| S4 | No orphans: names are recorded before `CREATE`, a failed run drops what it created | [template](internal/template/build_integration_test.go) `TestInvariant_S4_FailedMigrateLeavesNothing`, [engine](internal/engine/up_integration_test.go) `TestInvariant_S4_UpRollsBack`, [e2e](test/e2e/failure_e2e_test.go) `TestE2E_MigrateFailureLeavesNoOrphan` |
| S5 | `REASSIGN OWNED` runs only `BY <app>_wt_owner`, after a guard proves it owns no database or tablespace | [backend](internal/backend/invariants_integration_test.go) `TestInvariant_S5_ReassignOnlyAfterGuard` |
| S6 | Secrets never appear in argv, the registry, logs or `up`/`status` JSON | [e2e](test/e2e/invariants_e2e_test.go) `TestInvariant_S6_SecretsNeverLeak` |
| S7 | Env files are edited only inside the marker block; symlinks, paths outside the worktree and corrupt blocks are refused; files are written atomically at 0600 | [envfile](internal/envfile/envfile_test.go) `TestInvariant_S7_EnvWriteRefusesUnsafeTargets` |
| S8 | Ports are never taken, reserved, ephemeral, below 1024 or docker-bound; port holders are never killed | [ports](internal/ports/ports_test.go) `TestInvariant_S8_PortAllocation` |
| S9 | No `rm -rf` of computed paths; directories go only through `git worktree remove` on listed, non-main, unlocked worktrees | [hooks](internal/hooks/claude_remove_test.go) `TestInvariant_S9_RemoveRefusals`, [cli](internal/cli/cmd_hook_test.go) `TestInvariant_S9_DownRemoveWorktreeRefusesMain` |
| S10 | The registry is updated under `flock` with atomic writes; no network, docker or migration work under the lock | [registry](internal/registry/registry_test.go) `TestInvariant_S10_RegistryConcurrency`, [engine](internal/engine/up_integration_test.go) `TestInvariant_S10_NoSlowWorkUnderLock` |
| S11 | Re-running is idempotent: same names, ports and password, byte-identical env files, `actions: []` | [engine](internal/engine/up_integration_test.go) `TestInvariant_S11_UpIdempotent`, [backend](internal/backend/database_integration_test.go) `TestInvariant_S11_CloneIdempotent` |
| S12 | Managed containers publish on loopback only | [cluster](internal/cluster/cluster_test.go) `TestInvariant_S12_LoopbackOnly`, [cluster](internal/cluster/managed_integration_test.go) `TestInvariant_S12_ManagedLifecycle` |
| S13 | `gc` is a dry run unless `--yes` and never touches locked or non-prunable worktrees | [engine](internal/engine/gc_integration_test.go) `TestInvariant_S13_GCDryRunDefaultNeverLocked`, [template](internal/template/gc_integration_test.go) `TestInvariant_S13_TemplateGCDryRunThenApply` |

The end-to-end suite in [`test/e2e`](test/e2e) drives the real binary: three `git worktree add`
through the hook plus one Claude Code `WorktreeCreate` payload on stdin, 16 parallel `up`
on a cold template (exactly one build), a failing migration (no orphan), `gc` after `rm -rf`,
and a live session on a "dev" database that must survive all of it.

## Benchmark

`bench/run.sh` builds a synthetic but realistic fixture ([`bench/gen_fixture.sh`](bench/gen_fixture.sh):
60 migrations, each one table with a foreign key, indexes, a view, a trigger and 5,000 seed
rows), then compares the ways to get a migrated database for a new worktree. It starts its
own throwaway container (or uses `BENCH_ADMIN_DSN`) and removes everything afterwards.

PostgreSQL 17.11 (`postgres:17-alpine`), 82 MB template, Intel i5-12500H laptop, NVMe,
measured 2026-10-31 on a machine also running ~50 other containers:

| Way to get a migrated per-worktree database | runs | p50 | min | max |
|---|---|---|---|---|
| `worktree-pg up`, warm template (role + clone + hand-over + ports + env) | 10 | **1587 ms** | 955 ms | 2535 ms |
| of which: `CREATE DATABASE ... TEMPLATE` alone | 10 | 469 ms | 439 ms | 2445 ms |
| `CREATE DATABASE` + all migrations + seed | 5 | 18574 ms | 17538 ms | 20242 ms |
| empty container per worktree + migrate | 3 | 21460 ms | 21196 ms | 22371 ms |
| `worktree-pg up`, cold (build the template once, then clone) | 1 | 21480 ms | | |

The gap grows with your migration history and shrinks with database size: a clone copies
files, so a multi-GB seed costs seconds (use `strategy = "file_copy"` above ~500 MB, see
[docs/spikes.md](docs/spikes.md)). Run `bench/run.sh` on your own machine; numbers from a busy
laptop are orders of magnitude, not promises.

## How it compares

All of these are good tools that solve neighbouring problems, and worktree-pg borrows
ideas from several of them. This table reflects our reading of their docs and source on
2026-10-31; corrections are welcome.

| | worktree-pg | [wtdb](https://github.com/willhackett/wtdb) | [workz](https://github.com/rohansx/workz) | [worktrunk](https://github.com/max-sixty/worktrunk) DB recipe | [Neon](https://neon.com/docs/introduction/branching) branching |
|---|---|---|---|---|---|
| What you get per worktree | DB cloned from a migrated template + own role + ports + env | DB copied from your dev DB | worktree manager with ports, optional DB copy or container | a fresh `postgres` container with an empty DB | copy-on-write branch of a cloud database |
| Migrations | run once per fingerprint | inherited from the dev DB | inherited from the source DB | you run them per container | inherited from the parent branch |
| Source of the copy | dedicated template, never connected | your live dev DB | your live dev DB | none | parent branch |
| Effect on your dev DB's sessions | none | terminates them before copying | copy fails while the dev server is connected | none | none |
| Per-worktree login role | yes (or `shared` mode) | no | no | n/a (one superuser per container) | roles copied from the parent |
| Port allocation | sticky, registry-backed, collision-checked | n/a | registry | hashed, unchecked | n/a |
| Runs where | any Postgres 15+, or a managed local container | any Postgres | local | Docker | Neon cloud only |
| Scope | DB layer only; meant to be called by worktree managers | DB only | full worktree workflow incl. MCP | full worktree workflow | hosted Postgres platform |

Prior art and credits: **wtdb** showed that template cloning per worktree is the right
primitive. **workz** pioneered a port registry and Claude Code worktree hooks for this
workflow. **worktrunk** made worktree-per-agent mainstream; `wt-pg` is a small shim for it.
**Neon** is the better answer if your database already lives there. If you already use
workz or worktrunk, worktree-pg is designed to sit underneath them, not replace them.

## FAQ

**Isn't this a 30-line script?** The clone is one line. The rest is making it safe to run
from 16 agents at once: a template nobody connects to, a lock so it is built once,
rollback so a failed migration leaves nothing behind, ports that never collide, env files
edited without clobbering yours, and cleanup that can never drop a database it did not make.

**Does it ever touch my dev database?** No. The template is built from `template1` by
running your migrate command. worktree-pg only terminates sessions on its own template
build database, and only drops objects that carry its marker and match its registry.

**What privileges does the admin role need?** `CREATEDB` plus `CREATEROLE` for per-worktree
roles, or only `CREATEDB` with `roles = "shared"`. A superuser is needed only for untrusted
extensions. `worktree-pg doctor` tells you what is missing. Managed services such as RDS,
Cloud SQL and Neon give you such a role.

**I changed a migration. What happens to existing worktrees?** New worktrees get a new
template. Existing clones keep their data; `status` marks them `stale` so you can migrate
them in place or re-create them with `down` + `up`.

**I deleted a worktree with `rm -rf`.** Run `git worktree prune`, then `worktree-pg gc`
to see what would be dropped, and `gc --yes` to drop it.

**Can I run Postgres in docker without managing it myself?** Yes: `[cluster] source = "docker"`
starts one labelled container per image, bound to loopback, with its data in a named volume.

**Multiple databases per worktree?** Several `[[dsn]]` entries (for example app and RLS
roles) can point at the worktree's database today. A separate cloned test database per
worktree is planned.

**Windows?** Not yet. Linux is the main target, macOS is best effort.

## Contributing

Bug reports with `worktree-pg doctor --json` output, docs fixes and new examples are very
welcome. Small first contributions (a migration-tool example, a shell format, an adapter for
another worktree manager) touch one file plus one test. See [CONTRIBUTING.md](CONTRIBUTING.md),
and [SECURITY.md](SECURITY.md) for reporting vulnerabilities privately.

## License

[MIT](LICENSE) © 2026 LysCongThanh
