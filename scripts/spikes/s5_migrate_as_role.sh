#!/usr/bin/env bash
# Spike S5: goose + Medusa/MikroORM-style SQL migrations run as the per-worktree role on a clone;
# trusted vs untrusted extensions for a NOSUPERUSER admin and for the owner role.
# Usage: s5_migrate_as_role.sh <port> <goose-binary>
set -u
PORT=$1; GOOSE=$2
D=$(mktemp -d); trap 'rm -rf "$D"' EXIT
su()  { PGPASSWORD=$PGPASSWORD_SU psql -X -q -h 127.0.0.1 -p "$PORT" -U postgres -v ON_ERROR_STOP=1 "$@"; }
adm() { PGPASSWORD=admpw psql -X -q -h 127.0.0.1 -p "$PORT" -U s5admin -v ON_ERROR_STOP=1 "$@"; }
as()  { local r=$1; shift; PGPASSWORD=rolepw psql -X -q -h 127.0.0.1 -p "$PORT" -U "$r" -v ON_ERROR_STOP=1 "$@"; }
MAJOR=$(su -Atc "select current_setting('server_version_num')::int/10000" postgres)
G16=""; [ "$MAJOR" -ge 16 ] && G16=" with inherit true, set true"
echo "== PG $MAJOR"
su postgres -c "update pg_database set datistemplate=false where datname like 's5_%'" >/dev/null
su postgres -Atc "select format('drop database %I with (force);', datname) from pg_database where datname like 's5_%'" | su postgres
su postgres -c "drop role if exists s5_wt_a" -c "drop role if exists s5_wt_owner" -c "drop role if exists s5admin" -c "create role s5admin login password 'admpw' createdb createrole"
adm postgres -c "create role s5_wt_owner login password 'rolepw'" -c "grant s5_wt_owner to s5admin$G16" -c "create database s5_tpl_build"
adm s5_tpl_build -c "alter schema public owner to s5_wt_owner" -c "grant create, temp on database s5_tpl_build to s5_wt_owner"
echo "-- extensions as NOSUPERUSER admin (db owner): pgcrypto(trusted) citext(trusted) pg_stat_statements(untrusted)"
for e in pgcrypto citext pg_stat_statements; do adm s5_tpl_build -c "create extension if not exists $e" 2>&1 | sed "s/^/   $e: /" && true; done
adm s5_tpl_build -Atc "select '   installed: '||string_agg(extname||'(owner '||pg_get_userbyid(extowner)||')',', ') from pg_extension where extname<>'plpgsql'"
echo "-- extension as owner role (has CREATE on db): uuid-ossp(trusted)"
as s5_wt_owner s5_tpl_build -c 'create extension if not exists "uuid-ossp"' 2>&1 | sed 's/^/   /'
mkdir -p "$D/goose" "$D/medusa"
cat > "$D/goose/00001_init.sql" <<'SQL'
-- +goose Up
create table users(id uuid primary key default gen_random_uuid(), email citext unique not null);
-- +goose Down
drop table users;
SQL
cat > "$D/medusa/Migration20260101.sql" <<'SQL'
create table if not exists "mikro_orm_migrations" ("id" serial primary key, "name" varchar(255), "executed_at" timestamptz default current_timestamp);
create table if not exists "product" ("id" text not null, "title" text not null, "metadata" jsonb null, "deleted_at" timestamptz null, constraint "product_pkey" primary key ("id"));
create index if not exists "IDX_product_deleted_at" on "product" ("deleted_at") where deleted_at is null;
insert into mikro_orm_migrations(name) values ('Migration20260101');
SQL
URL="postgres://s5_wt_owner:rolepw@127.0.0.1:$PORT/s5_tpl_build?sslmode=disable"
"$GOOSE" -dir "$D/goose" postgres "$URL" up 2>&1 | tail -1 | sed 's/^/   goose(owner,build): /'
as s5_wt_owner s5_tpl_build -f "$D/medusa/Migration20260101.sql" && echo "   OK medusa-like (owner,build)"
adm postgres -c "alter database s5_tpl_build allow_connections false" -c "alter database s5_tpl_build rename to s5_tpl" -c "alter database s5_tpl is_template true"
adm postgres -c "create role s5_wt_a login password 'rolepw'" -c "grant s5_wt_a to s5admin$G16" -c "create database s5_wt_a template s5_tpl owner s5_wt_a"
adm s5_wt_a -Atc "select format('ALTER SCHEMA %I OWNER TO s5_wt_a;', nspname) from pg_namespace where nspowner='s5_wt_owner'::regrole" | adm s5_wt_a
adm s5_wt_a -c "reassign owned by s5_wt_owner to s5_wt_a"
cat > "$D/goose/00002_more.sql" <<'SQL'
-- +goose Up
alter table users add column name text;
create table posts(id bigserial primary key, user_id uuid references users(id), body text, h text default encode(digest('x','sha256'),'hex'));
-- +goose Down
drop table posts; alter table users drop column name;
SQL
cat > "$D/medusa/Migration20260102.sql" <<'SQL'
alter table if exists "product" add column if not exists "handle" text not null default '';
create unique index if not exists "IDX_product_handle_unique" on "product" ("handle") where deleted_at is null;
insert into mikro_orm_migrations(name) values ('Migration20260102');
SQL
URL="postgres://s5_wt_a:rolepw@127.0.0.1:$PORT/s5_wt_a?sslmode=disable"
"$GOOSE" -dir "$D/goose" postgres "$URL" up 2>&1 | tail -1 | sed 's/^/   goose(wt role,clone): /'
"$GOOSE" -dir "$D/goose" postgres "$URL" down 2>&1 | tail -1 | sed 's/^/   goose down(wt role,clone): /'
as s5_wt_a s5_wt_a -f "$D/medusa/Migration20260102.sql" && echo "   OK medusa-like (wt role,clone)"
as s5_wt_a s5_wt_a -c 'create extension if not exists "uuid-ossp"' -c "alter extension citext update" 2>&1 | sed 's/^/   ext ops as wt role: /'
echo "-- cleanup"; adm postgres -c "drop database s5_wt_a with (force)" -c "drop role s5_wt_a" -c "alter database s5_tpl is_template false" -c "drop database s5_tpl"
