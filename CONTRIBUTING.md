# Contributing to worktree-pg

Thanks for helping! Bug reports, docs fixes, examples for your stack and small focused
pull requests are all welcome. For anything bigger than a bug fix, please open an issue or
a Discussion first so we can agree on the shape before you write code.

## Setup in three commands

You need Go 1.25+, git, docker and `psql` (the integration tests start throwaway Postgres
containers on `127.0.0.1:15415-15418`).

```sh
git clone https://github.com/LysCongThanh/worktree-pg && cd worktree-pg
make test          # unit tests, no database needed (seconds)
make itest e2e     # real Postgres 15-18 in docker + end-to-end runs of the built binary
```

`make itest` leaves its `wtpg-test-pg*` containers running so later runs are fast; remove
them with `docker rm -f wtpg-test-pg15 wtpg-test-pg16 wtpg-test-pg17 wtpg-test-pg18`.
To run one suite against one server:

```sh
export WTPG_TEST_ADMIN_DSN=postgres://postgres:postgres@127.0.0.1:15417/postgres?sslmode=disable
go test -tags integration ./internal/engine/ -run TestInvariant_S4 -v
go test -tags e2e ./test/e2e/ -run TestE2E_FullScenario -v
```

Before you push: `make lint` (gofmt + go vet for every build tag).

## Package map

Everything lives under `internal/`; `main.go` only calls `cli.Main`. Arrows point at what a
package may use. Pure packages (no I/O) are marked * and are the easiest place to start.

| Package | Job |
|---|---|
| `cli` | flag parsing, one function per command, `--json` envelope, exit codes |
| `hooks` | Claude Code `claude-hook create/remove`, git `post-checkout`, `hook install` |
| `doctor` | read-only prerequisite checks |
| `engine` | the only API the CLI and hooks call: `Up`, `Down`, `Status`, `Env`, `GC`, template commands |
| `backend` | turns a spec into a database + role (`Ensure`/`Destroy`/`Inspect`) |
| `template` | fingerprint, build under advisory lock, refresh, template gc |
| `cluster`, `docker` | external server or the managed docker container; `cluster_setup_cmd` |
| `pg` | admin connection, capability probe, DDL helpers, clone retry, ownership hand-over, DSN rendering |
| `registry`, `lockfile` | `state.json` under `flock`, atomic writes, per-worktree leases |
| `ports` * | sticky port allocation (`Allocator` with a `Probe` seam for tests) |
| `envfile` * | splice/strip the managed block, dotenv and shell quoting |
| `names` * | object names, 63-byte fitting, markers |
| `config` | `.worktree-pg.toml` load, overlay, validation |
| `gitx` * (parser) | `git worktree list --porcelain -z` parser, worktree add/remove, `.worktreeinclude` |
| `secret`, `runner`, `errs` | HMAC passwords, running user commands with logs, typed errors |

Design notes and the reasons behind them: [docs/safety.md](docs/safety.md) and
[docs/spikes.md](docs/spikes.md).

## Good first contributions

Each of these is one file plus one test:

### Add an example for your migration tool (a "detector" in waiting)

`worktree-pg init` with automatic detection of migration tools is planned. Until then, the
best way to teach worktree-pg about a tool (Prisma, Alembic, Flyway, Rails, sqlx, Atlas...) is
a working example:

1. Create `examples/<stack>/.worktree-pg.toml` with the right `inputs`, `migrate_cmd` and
   env vars for that tool. The migrate command gets `PG*` variables and every `[[dsn]]`
   variable pointing at the template, and must never need a password on its command line.
2. `internal/config/config_test.go` loads every example; update the expected count.
3. Mention it in the README list of ready-made configs.

When detectors land they will live in one file each with a table test, and these examples
become their fixtures.

### Add a shell format to `env`

`env --format` supports `dotenv`, `shell` and `json`. To add, say, `fish`:

1. Add `RenderFish(vars []Var) []string` and its quoting next to `RenderShell` in
   `internal/envfile/render.go`.
2. Accept the name in `internal/cli/cmd_env_run.go` (`cmdEnv`).
3. Add table cases to `internal/envfile/envfile_test.go`, including values with quotes,
   spaces, `$` and newlines.

### Add an adapter for another worktree manager

If the tool has post-create / pre-remove hooks, the smallest adapter is a documented call
to [`examples/wt-pg`](examples/wt-pg) in [docs/agents.md](docs/agents.md). If it passes
structured data (like Claude Code's stdin JSON), add a file in `internal/hooks/` that parses
the input, calls `engine.Up`/`engine.Down`, and follows the same rules as
`claude_create.go`: roll back only what this run created, never remove a directory except
with `git worktree remove` on an eligible worktree (invariant S9), keep stdout for the
contract the tool expects. Add a piped-stdin test on a temp git repo.

## Rules of the road

- **Safety first.** If a change touches dropping, altering, terminating, env writing or port
  choice, find the matching invariant in [docs/safety.md](docs/safety.md) and extend its
  `TestInvariant_*` test.
- **Real databases in tests.** The database layer is never mocked. Integration tests run on
  PG 15-18 as a superuser and as a `NOSUPERUSER CREATEDB CREATEROLE` admin. Do not skip a
  failing test to get green.
- **Small files.** Keep Go files under about 200 lines, use snake_case file names, and write
  comments that explain why.
- **Dependencies.** The only runtime dependencies are pgx and BurntSushi/toml. Adding one
  needs a very good reason.
- **Commits.** Conventional commits (`feat:`, `fix:`, `test:`, `docs:`...), one logical change
  per commit.

## AI-assisted pull requests

This project is built for people working with coding agents, and AI-assisted contributions
are welcome on the same terms as any other:

- **You are the author.** You must have read and understood every line you submit, and be
  able to explain and change it during review.
- **Say so.** If an AI tool wrote a meaningful part of the change, tick the box in the PR
  template and mention which parts. This is not held against you; it helps reviewers.
- **Run it yourself.** Paste the real commands you ran. Tests the PR adds must fail without
  the change.
- **No drive-by mass PRs.** Generated sweeping refactors, reformatting or "improvements"
  without a linked issue will be closed.
- Issues and Discussion answers should be written by you. Do not paste unverified model
  output as an answer to someone else's question.

## Code of conduct

Be kind and assume good faith. Harassment or personal attacks are not tolerated; maintainers
may remove content or block participants who do this.
