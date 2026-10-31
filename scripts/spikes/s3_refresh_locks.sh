#!/usr/bin/env bash
# Spike S3: template gc/refresh while clones are connected; DROP/RENAME template during an in-flight CREATE DATABASE.
# Usage: s3_refresh_locks.sh <port>
set -u
PORT=$1
su() { PGPASSWORD=$PGPASSWORD_SU psql -X -q -h 127.0.0.1 -p "$PORT" -U postgres -v ON_ERROR_STOP=1 "$@"; }
echo "== PG $(su -Atc 'show server_version' postgres)"
su postgres -c "update pg_database set datistemplate=false where datname like 's3_%'" >/dev/null
su postgres -Atc "select format('drop database %I with (force);', datname) from pg_database where datname like 's3_%'" | su postgres
su postgres -c "create database s3_tpl"
su s3_tpl -c "create table big as select g id, repeat(md5(g::text),8) pad from generate_series(1,1000000) g"
su postgres -c "alter database s3_tpl allow_connections false" -c "alter database s3_tpl is_template true" -c "create database s3_clone template s3_tpl"
echo "-- 1) clone has a live session; gc template (is_template false + drop, lock_timeout 5s):"
su s3_clone -c "select pg_sleep(15)" >/dev/null & sleep 0.5
t=$(date +%s%N); su postgres -c "set lock_timeout='5s'" -c "alter database s3_tpl is_template false" -c "drop database s3_tpl" && echo "   OK dropped template while clone in use, ms=$(( ($(date +%s%N)-t)/1000000 ))"
su postgres -Atc "select '   clone session still alive: '||count(*) from pg_stat_activity where datname='s3_clone'"
wait
echo "-- 2) in-flight CREATE DATABASE (wal_log) vs DROP/RENAME template with lock_timeout=300ms:"
su postgres -c "create database s3_tpl2 template s3_clone" -c "alter database s3_tpl2 allow_connections false" -c "alter database s3_tpl2 is_template true"
su postgres -c "create database s3_inflight template s3_tpl2" & sleep 0.2
su postgres -c "set lock_timeout='300ms'" -c "alter database s3_tpl2 rename to s3_tpl2_x" 2>&1 | sed 's/^/   RENAME: /'
su postgres -c "set lock_timeout='300ms'" -c "alter database s3_tpl2 is_template false" -c "drop database s3_tpl2" 2>&1 | sed 's/^/   DROP: /'
wait; su postgres -Atc "select '   in-flight clone created: '||count(*) from pg_database where datname='s3_inflight'"
su postgres -Atc "select '   template still template: '||datistemplate from pg_database where datname='s3_tpl2'"
echo "-- 3) DROP template holds lock -> concurrent CREATE waits then fails (template gone):"
su postgres -c "alter database s3_tpl2 is_template false"
( su postgres -c "begin" -c "select 1" -c "commit" >/dev/null )
su postgres -c "drop database s3_tpl2" & sleep 0.05; su postgres -c "create database s3_late template s3_tpl2" 2>&1 | sed 's/^/   CREATE after DROP: /'; wait
echo "-- cleanup"; su postgres -Atc "select format('drop database %I with (force);', datname) from pg_database where datname like 's3_%'" | su postgres
