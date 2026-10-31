#!/usr/bin/env bash
# Benchmark: per-worktree database via template clone (`worktree-pg up`) versus
# migrating from scratch (CREATE DATABASE + run every migration + seed), and
# optionally the "empty container per worktree" recipe (docker run + migrate).
#
#   bench/run.sh                      # starts wtpg-test-bench on 127.0.0.1:15490 (postgres:17-alpine)
#   BENCH_ADMIN_DSN=postgres://... bench/run.sh
# Knobs: PG_IMAGE N_CLONE=10 N_SCRATCH=5 N_CONTAINER=3 (0 = skip) TABLES=60 ROWS=5000
# Prints a markdown table. Everything it creates is removed on exit.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
repo_root=$(cd "$here/.." && pwd)
PG_IMAGE=${PG_IMAGE:-postgres:17-alpine}
N_CLONE=${N_CLONE:-10} N_SCRATCH=${N_SCRATCH:-5} N_CONTAINER=${N_CONTAINER:-3}
TABLES=${TABLES:-60} ROWS=${ROWS:-5000}
export POSTGRES_PASSWORD=${BENCH_PASSWORD:-bench} # passed by name to docker, never argv

work=$(mktemp -d "${TMPDIR:-/tmp}/wtpg-bench-XXXXXX")
containers=()
cleanup() {
  [ -n "${proj:-}" ] && for d in "$work"/wt*; do [ -d "$d" ] && (cd "$d" && "$bin" down -q >/dev/null 2>&1 || true); done
  if [ -n "${proj:-}" ] && [ -n "${dsn:-}" ]; then # also drop the template + owner role it built
    for db in bench_scratch bench_raw $(psql -X -Atqc "select datname from pg_database where datname like 'bench\_tpl\_%'" "$dsn" 2>/dev/null); do
      psql -X -q "$dsn" -c "ALTER DATABASE $db IS_TEMPLATE false" >/dev/null 2>&1 || true
      psql -X -q "$dsn" -c "DROP DATABASE IF EXISTS $db WITH (FORCE)" >/dev/null 2>&1 || true
    done
    psql -X -q "$dsn" -c "DROP ROLE IF EXISTS bench_wt_owner" >/dev/null 2>&1 || true
  fi
  for c in "${containers[@]}"; do docker rm -f "$c" >/dev/null 2>&1 || true; done
  rm -rf "$work"
}
trap cleanup EXIT

now_ms() { date +%s%3N; }
p50() { sort -n | awk '{a[NR]=$1} END {print (NR%2 ? a[(NR+1)/2] : int((a[NR/2]+a[NR/2+1])/2))}'; }
wait_ready() { # host TCP SELECT 1 (never socket pg_isready; see docs/spikes.md D4)
  for _ in $(seq 1 240); do psql -X -Atqc 'select 1' "$1" >/dev/null 2>&1 && return 0; sleep 0.25; done
  echo "postgres at $1 not ready" >&2; return 1
}
start_pg() { # name port -> waits until ready
  docker rm -f "$1" >/dev/null 2>&1 || true
  docker run -d --name "$1" --label worktree-pg=bench -e POSTGRES_PASSWORD -p "127.0.0.1:$2:5432" "$PG_IMAGE" >/dev/null
  containers+=("$1")
}

bin="$work/worktree-pg"
(cd "$repo_root" && go build -o "$bin" .)
dsn=${BENCH_ADMIN_DSN:-}
if [ -z "$dsn" ]; then
  start_pg wtpg-test-bench 15490
  dsn="postgres://postgres:$POSTGRES_PASSWORD@127.0.0.1:15490/postgres?sslmode=disable"
fi
wait_ready "$dsn"
server=$(psql -X -Atqc 'show server_version' "$dsn")

# Fixture project: a git repo whose template is built by psql-applied migrations.
proj="$work/app"
"$here/gen_fixture.sh" "$proj" "$TABLES" "$ROWS" >&2
migrate='for f in db/migrations/*.sql; do psql -X -q -v ON_ERROR_STOP=1 -f "$f" || exit 1; done'
cat > "$proj/.worktree-pg.toml" <<TOML
version = 1
name = "bench"
[template]
inputs = ["db"]
migrate_cmd = '$migrate'
[[dsn]]
var = "DATABASE_URL"
role = "@owner"
[env]
files = []
TOML
export WORKTREE_PG_STATE_DIR="$work/state" WORKTREE_PG_ADMIN_DSN="$dsn"
export GIT_CONFIG_GLOBAL=/dev/null GIT_AUTHOR_NAME=b GIT_AUTHOR_EMAIL=b@example.com GIT_COMMITTER_NAME=b GIT_COMMITTER_EMAIL=b@example.com
(cd "$proj" && git init -q -b main && git add -A && git commit -qm fixture)

# 1) First up: builds the template (one migrate run) and clones it.
git -C "$proj" worktree add -q -b wt0 "$work/wt0"
t0=$(now_ms); (cd "$work/wt0" && "$bin" up -q --wait 30m); cold=$(( $(now_ms) - t0 ))
tpl=$(psql -X -Atqc "select datname from pg_database where datname like 'bench\_tpl\_%' and datistemplate" "$dsn")
size=$(psql -X -Atqc "select pg_size_pretty(pg_database_size('$tpl'))" "$dsn")

# 2) Warm template: `worktree-pg up` per new worktree (role + clone + hand-over + env).
clone=()
for i in $(seq 1 "$N_CLONE"); do
  git -C "$proj" worktree add -q -b "wt$i" "$work/wt$i"
  t0=$(now_ms); (cd "$work/wt$i" && "$bin" up -q); clone+=($(( $(now_ms) - t0 )))
done

# 2b) The clone alone: CREATE DATABASE ... TEMPLATE, without roles/ports/env.
raw=()
for i in $(seq 1 "$N_CLONE"); do
  t0=$(now_ms); psql -X -q "$dsn" -c "CREATE DATABASE bench_raw TEMPLATE $tpl" >/dev/null; raw+=($(( $(now_ms) - t0 )))
  psql -X -q "$dsn" -c "DROP DATABASE bench_raw" >/dev/null
done

# 3) Migrate from scratch into an empty database, as a per-worktree script would.
scratch_dsn=$(echo "$dsn" | sed -E 's#(://[^/]*)/[^?]*#\1/bench_scratch#')
scratch=()
for _ in $(seq 1 "$N_SCRATCH"); do
  t0=$(now_ms)
  psql -X -q "$dsn" -c "CREATE DATABASE bench_scratch" >/dev/null
  (cd "$proj" && for f in db/migrations/*.sql; do psql -X -q -v ON_ERROR_STOP=1 -f "$f" "$scratch_dsn" >/dev/null; done)
  scratch+=($(( $(now_ms) - t0 )))
  psql -X -q "$dsn" -c "DROP DATABASE bench_scratch WITH (FORCE)" >/dev/null
done

# 4) Empty container per worktree: docker run + readiness + migrate.
cont=()
for i in $(seq 1 "$N_CONTAINER"); do
  name="wtpg-test-bench-c$i" port=$((15490 + i))
  t0=$(now_ms); start_pg "$name" "$port"
  cdsn="postgres://postgres:$POSTGRES_PASSWORD@127.0.0.1:$port/postgres?sslmode=disable"
  wait_ready "$cdsn"
  (cd "$proj" && for f in db/migrations/*.sql; do psql -X -q -v ON_ERROR_STOP=1 -f "$f" "$cdsn" >/dev/null; done)
  cont+=($(( $(now_ms) - t0 )))
  docker rm -f "$name" >/dev/null; containers=("${containers[@]/$name}")
done

cpu=$(grep -m1 'model name' /proc/cpuinfo 2>/dev/null | cut -d: -f2 | sed 's/^ //' || uname -m)
echo
echo "PostgreSQL $server ($PG_IMAGE), fixture $TABLES tables / $((TABLES * ROWS)) rows, template $size, $cpu"
echo
echo "| Way to get a migrated per-worktree database | runs | p50 | min | max |"
echo "|---|---|---|---|---|"
row() { local label=$1; shift; [ $# -eq 0 ] && return; printf '| %s | %d | %s ms | %s ms | %s ms |\n' "$label" $# \
  "$(printf '%s\n' "$@" | p50)" "$(printf '%s\n' "$@" | sort -n | head -1)" "$(printf '%s\n' "$@" | sort -n | tail -1)"; }
row '`worktree-pg up`, warm template (clone + role + env)' "${clone[@]}"
row 'of which: `CREATE DATABASE ... TEMPLATE` alone' "${raw[@]}"
row 'CREATE DATABASE + all migrations + seed' "${scratch[@]}"
row 'empty container per worktree + migrate' "${cont[@]}"
row '`worktree-pg up`, cold (build template once, then clone)' "$cold"
