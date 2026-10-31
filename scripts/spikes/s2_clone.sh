#!/usr/bin/env bash
# Spike S2: non-owner clone of an at-rest template, 10 parallel clones, WAL_LOG vs FILE_COPY timing.
# Usage: s2_clone.sh <port> <size_mb>
set -u
PORT=$1; MB=${2:-100}
su()  { PGPASSWORD=$PGPASSWORD_SU psql -X -q -h 127.0.0.1 -p "$PORT" -U postgres -v ON_ERROR_STOP=1 "$@"; }
oth() { PGPASSWORD=othpw psql -X -q -h 127.0.0.1 -p "$PORT" -U s2other -v ON_ERROR_STOP=1 "$@"; }
ms()  { echo $(( ($(date +%s%N)-$1)/1000000 )); }
echo "== PG $(su -Atc 'show server_version' postgres) template ${MB}MB"
su postgres -Atc "select format('drop database %I with (force);', datname) from pg_database where datname like 's2_%'" | sed 's/^/update pg_database set datistemplate=false where datname like '"'"'s2_%'"'"'; /' | su postgres >/dev/null 2>&1
su postgres -c "drop role if exists s2other" -c "create role s2other login password 'othpw' createdb" -c "create database s2_tpl_build"
su s2_tpl_build -c "create table big as select g as id, repeat(md5(g::text), 8) as pad from generate_series(1, ($MB*1024*1024/300)) g" -c "create index on big(id)"
su postgres -c "alter database s2_tpl_build allow_connections false" -c "alter database s2_tpl_build rename to s2_tpl" -c "alter database s2_tpl is_template true"
echo "   template size: $(su postgres -Atc "select pg_size_pretty(pg_database_size('s2_tpl'))")"
echo "-- non-owner CREATEDB role clones template (datistemplate=t, datallowconn=f):"
oth postgres -c "create database s2_other template s2_tpl" && echo "   OK non-owner clone"
for s in wal_log file_copy; do
  for i in 1 2; do t=$(date +%s%N); su postgres -c "create database s2_${s}_$i template s2_tpl strategy $s"; echo "   strategy=$s run$i ms=$(ms $t)"; done
done
echo "-- 10 parallel clones (wal_log):"
t=$(date +%s%N); for i in $(seq 1 10); do su postgres -c "create database s2_par_$i template s2_tpl" 2>&1 | sed "s/^/   par$i: /" & done; wait
echo "   10 parallel total ms=$(ms $t); created=$(su postgres -Atc "select count(*) from pg_database where datname like 's2_par_%'")"
echo "-- cleanup"
su postgres -Atc "select format('drop database %I with (force);', datname) from pg_database where datname like 's2_%' and not datistemplate" | su postgres
su postgres -c "alter database s2_tpl is_template false" -c "drop database s2_tpl" -c "drop role s2other"
