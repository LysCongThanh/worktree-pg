# Spikes (M0)

Run 2026-10-31 on Linux (ext4, NVMe), docker 29, local images `postgres:15-alpine` (15.19),
`postgres:16` (16.15), `postgres:17-alpine` (17.11), `postgres:18-alpine` (18.6), each published on
`127.0.0.1:15415-15418`. git 2.55.0, Claude Code 2.1.316, goose v3.29.0.
Scripts live in `scripts/spikes/` and can be re-run: `PGPASSWORD_SU=<pw> scripts/spikes/s1_reassign.sh 15416`.
Timings were taken on a busy developer machine; treat them as orders of magnitude.

## Design changes found

| # | Finding | Change |
|---|---|---|
| D1 | `REASSIGN OWNED BY owner TO r` run by a NOSUPERUSER admin fails with `permission denied for schema public` on 15-18. Changing a table's owner requires the new owner to hold CREATE on the schema, and that check is skipped only for superusers. | Before REASSIGN, run `ALTER SCHEMA s OWNER TO r` for every schema owned by `<app>_wt_owner` in the clone (r owns the database, so it has CREATE on it). Then REASSIGN. Works on 15-18 as superuser and as non-superuser. |
| D2 | PG16+: a CREATEROLE admin gets only ADMIN OPTION on roles it creates (`pg_has_role` USAGE=f, SET=f, MEMBER=t). `CREATE DATABASE ... OWNER r` then fails with `must be able to SET ROLE "r"`. | Confirmed: `GRANT r TO CURRENT_USER WITH INHERIT TRUE, SET TRUE` is required on 16+ (`pg.ServerInfo.NeedsSetGrant`). PG15 uses a plain `GRANT r TO CURRENT_USER`. |
| D3 | Template gc as `ALTER ... IS_TEMPLATE false` then `DROP` (with `lock_timeout`): when DROP times out because a clone is in flight, the template stays `IS_TEMPLATE false`. | gc must restore `IS_TEMPLATE true` when DROP fails. Retry later. |
| D4 | Docker readiness: `pg_isready` on the unix socket inside the container reports ready during the entrypoint's temporary init server, 2-4 s before host TCP works. The log shows "ready to accept connections" twice. | Readiness = a host TCP connection + `SELECT 1` with the real credentials (or the second "ready" log line). Never socket `pg_isready` alone. |
| D5 | `postgres:18` images declare `VOLUME /var/lib/postgresql` with `PGDATA=/var/lib/postgresql/18/docker`. 15-17 declare `/var/lib/postgresql/data`. | The managed cluster reads `Config.Volumes` from `docker image inspect` and mounts its named volume there. Do not hard-code the path. |
| D6 | `docker run -e POSTGRES_PASSWORD` (name only) keeps the secret out of the docker CLI argv. `docker inspect` still shows it in `Config.Env`. | Accepted. Docker-group access is root-equivalent anyway. Documented in docs/safety.md (M8). Optional later: `POSTGRES_PASSWORD_FILE`. |
| D7 | Untrusted extensions such as `pg_stat_statements` need a superuser admin. Extensions created by the admin stay admin-owned after REASSIGN, so the worktree role cannot `ALTER EXTENSION ... UPDATE`. | doctor warns when `[template].extensions` lists an untrusted extension and the admin is not superuser. Docs: create trusted extensions in migrations if the app must own them. |

## Results per spike

### S1 REASSIGN as a NOSUPERUSER `CREATEDB CREATEROLE` admin (15, 16, 17, 18)
- Flow: admin creates `s1_wt_owner`, then builds a DB from `template1` and runs `ALTER SCHEMA public OWNER TO owner` plus `GRANT CREATE, TEMP`. The owner then migrates: table, view, function, enum, RLS policy, `ALTER DEFAULT PRIVILEGES` for a cluster role. The build DB is then set `ALLOW_CONNECTIONS false`, renamed, and set `IS_TEMPLATE true`. Clone: `CREATE DATABASE ... TEMPLATE tpl OWNER r`, then the guard query, schema owner fix (D1), REASSIGN, and default-ACL replay.
- Guard `count(datdba=owner)+count(spcowner=owner)` = 0 on all versions.
- After REASSIGN, tables, views, functions, types and the public schema are owned by `r`. The template's `datdba` is unchanged (still the admin) and it stays `datistemplate=t`, `datallowconn=f`.
- `pg_default_acl` rows still name the owner role after REASSIGN (it does not move them). Replaying them with `ALTER DEFAULT PRIVILEGES FOR ROLE r ...` from `aclexplode(defaclacl)` works: a table created later by `r` is readable by the cluster role. Skip the grantee == owner entry, because it is the owner's implicit privileges. PG17+ shows the extra `m` (MAINTAIN) privilege in the owner entry.
- The worktree role can migrate: ALTER the existing table, CREATE TABLE, DROP and re-CREATE the RLS policy. A non-owner cluster role sees only its tenant's rows through RLS.
- `REVOKE CONNECT ... FROM PUBLIC`: `has_database_privilege(other,'CONNECT')` = f. Teardown with `DROP DATABASE ... WITH (FORCE)` then `DROP ROLE` succeeds.
- Clone time for a small template: 63-127 ms.

### S2 Non-owner clone, rename, parallel clones, strategy timing
- A plain CREATEDB role that does not own the template can clone an `IS_TEMPLATE true` + `ALLOW_CONNECTIONS false` template (15-18). Renaming the build DB works.
- 100 MB template (116 MB on disk), ms, run1/run2:

| PG | WAL_LOG | FILE_COPY | 10 parallel WAL_LOG (total) |
|---|---|---|---|
| 15 | 380 / 390 | 1290 / 276 | 8.6 s |
| 16 | 753 / 684 | 1294 / 276 | 4.1 s |
| 17 | 1184 / 1276 | 3497 / 808 | 11.0 s |
| 18 | 1113 / 1129 | 3393 / 714 | 12.8 s |

- 1 GB template (1120 MB): PG16 WAL_LOG 13.3 / 17.9 s, FILE_COPY 6.4 / 5.3 s, 10 parallel 343 s. PG18 ran while other disk-heavy spikes were running: WAL_LOG 59 / 96 s, FILE_COPY 52 / 46 s, 10 parallel 782 s.
- All 10 parallel clones succeeded every time (ShareLock on the template is compatible). The first FILE_COPY pays a checkpoint. Default stays `strategy=""` (server default WAL_LOG). Docs recommend `file_copy` for templates over ~500 MB. Parallel clones of large templates are bounded by disk: the engine should not parallelise them further.

### S3 Template gc/refresh vs clones
- Dropping a template while a clone has a live session: OK (about 0.8 s on 16/18). The clone session is unaffected because clones do not depend on their template.
- During an in-flight `CREATE DATABASE ... TEMPLATE t`, both `ALTER DATABASE t RENAME` and `DROP DATABASE t` fail after `lock_timeout` with `canceling statement due to lock timeout` (55P03), and the clone completes. See D3 for the side effect.
- When `DROP` holds the lock, a concurrent `CREATE ... TEMPLATE t` waits, then fails with `template database "t" does not exist`. The engine must re-resolve the template and retry under the advisory lock.

### S4 pgx error codes (15, 16)
- `CREATE DATABASE` via `PgConn().Exec(...).ReadAll()` (simple protocol) works.
- A session held on the source DB gives `*pgconn.PgError` Code `55006` after the server's own 5.0 s wait. The client retry must therefore budget about 5 s per attempt.
- `CREATE DATABASE` inside `conn.Begin()` fails with `25001`. A duplicate CREATE fails with `42P04`.

### S5 Migrations as the worktree role (15, 16, 17, 18)
- goose v3.29 `up` as the owner on the build DB, then `up`/`down` as the worktree role on the clone: OK. `goose_db_version` is reassigned like any other table.
- Medusa/MikroORM-style SQL migrations (`mikro_orm_migrations`, partial indexes, `ALTER TABLE IF EXISTS ... ADD COLUMN IF NOT EXISTS`) run as the owner, then as the worktree role: OK.
- Extensions: see D7. `pgcrypto` and `citext` (trusted) are created fine by the NOSUPERUSER admin. `pg_stat_statements` fails with `Must be superuser`. A trusted `uuid-ossp` created by the owner during migration is reassigned to the worktree role.

### S6 Docker managed cluster
- Volumes and readiness: see D4 and D5. Init to TCP-ready took 18-30 s on a loaded machine. Budget at least 60 s.
- The secret is not in the docker CLI argv (`/proc/<pid>/cmdline`) but is visible in `docker inspect` (D6).

### S7 Port probing vs docker
- `net.Listen(":p")` detects ports bound by docker-proxy on `127.0.0.1`, on `[::1]` and on `0.0.0.0`. `net.Listen("localhost:p")` misses a `[::1]`-only bind because Go resolves localhost to IPv4 first. So the `":p"` probe is the important one, and the spec's "both" rule is correct.
- A stopped container keeps `HostConfig.PortBindings` (for example `127.0.0.1:15421`) while `NetworkSettings.Ports` is empty, and `Listen` reports the port free. Excluding docker-bound ports through `docker inspect $(docker ps -aq)` is therefore required. It took 0.44 s for 49 containers.
- docker-proxy is enabled on this host. With `userland-proxy=false`, no listener exists at all and the inspect-based exclusion is the only protection.

### S8 Claude Code 2.1.316 `WorktreeCreate`
- Test: `claude --settings <json> -w s8probe -p ...` with a logging command hook.
- stdin: `{"session_id","transcript_path","cwd","hook_event_name":"WorktreeCreate","name":"s8probe"}`.
- `CLAUDE_PROJECT_DIR` is set and equals cwd (the main checkout).
- The hook printed noise and then the path. The session ran in the path from the last stdout line (the transcript `cwd` was the worktree). This confirms the "path as last stdout line" contract.
- `WorktreeRemove` did not fire when the `-p` session exited. Worktrees are kept, so `gc` and `down --remove-worktree` are needed (spec §6 already assumes this).

### S9 git 2.55 worktrees
- `git worktree remove` without `--force` succeeds when the worktree only contains ignored files (`.env`). Untracked files block it (`contains modified or untracked files`).
- A locked worktree is refused even with a single `--force` (`use 'remove -f -f'`). worktree-pg never passes `-f -f`.
- `git worktree list --porcelain -z` uses NUL after each attribute and an extra NUL per record. It shows `locked <reason>`, `prunable gitdir file points to non-existent location`, `detached`, and paths with spaces intact. Golden: `internal/gitx/testdata/porcelain_git255.z`.
