#!/usr/bin/env bash
# Spike S1: template build + clone + REASSIGN OWNED as a NOSUPERUSER CREATEDB CREATEROLE admin.
# Usage: s1_reassign.sh <port>   (container superuser postgres/$PGPASSWORD_SU on 127.0.0.1)
set -u
PORT=$1
su()   { PGPASSWORD=$PGPASSWORD_SU psql -X -q -h 127.0.0.1 -p "$PORT" -U postgres -v ON_ERROR_STOP=1 "$@"; }
adm()  { PGPASSWORD=admpw psql -X -q -h 127.0.0.1 -p "$PORT" -U s1admin -v ON_ERROR_STOP=1 "$@"; }
as()   { local r=$1; shift; PGPASSWORD=rolepw psql -X -q -h 127.0.0.1 -p "$PORT" -U "$r" -v ON_ERROR_STOP=1 "$@"; }
MAJOR=$(su -Atc "select current_setting('server_version_num')::int/10000" postgres)
echo "== PG $MAJOR"
su postgres -c "update pg_database set datistemplate=false where datname='s1_tpl'" >/dev/null
su postgres <<SQL
drop database if exists s1_wt_a with (force);
drop database if exists s1_wt_b with (force);
drop database if exists s1_tpl;
drop database if exists s1_tpl_build;
drop role if exists s1_wt_a; drop role if exists s1_wt_b;
drop role if exists s1_wt_owner; drop role if exists s1_reader; drop role if exists s1admin;
create role s1admin login password 'admpw' createdb createrole nosuperuser;
SQL
# cluster-wide role used by RLS + default privileges (cluster_setup_cmd analogue)
adm postgres -c "create role s1_reader nologin"
adm postgres -c "create role s1_wt_owner login password 'rolepw'"
if [ "$MAJOR" -ge 16 ]; then
  echo "-- PG16+: probe membership without INHERIT/SET (auto ADMIN only)"
  adm postgres -Atc "select pg_has_role('s1admin','s1_wt_owner','USAGE') usage, pg_has_role('s1admin','s1_wt_owner','SET') set_, pg_has_role('s1admin','s1_wt_owner','MEMBER') member"
  adm postgres -c "create database s1_probe owner s1_wt_owner" 2>&1 | sed 's/^/   probe CREATE DATABASE OWNER w\/o SET: /'
  adm postgres -c "grant s1_wt_owner to s1admin with inherit true, set true"
else
  adm postgres -c "grant s1_wt_owner to s1admin"
fi
adm postgres -c "drop database if exists s1_probe"
adm postgres -c "create database s1_tpl_build template template1"
adm postgres -c "comment on database s1_tpl_build is 'worktree-pg:v1 {\"kind\":\"template-build\"}'"
adm s1_tpl_build -c "alter schema public owner to s1_wt_owner" -c "grant create, temp on database s1_tpl_build to s1_wt_owner"
# migrations as owner role
as s1_wt_owner s1_tpl_build <<'SQL'
create table accounts(id serial primary key, tenant text not null, name text);
create view v_accounts as select * from accounts;
create function f_one() returns int language sql as 'select 1';
create type mood as enum ('ok','bad');
alter table accounts enable row level security;
create policy tenant_iso on accounts using (tenant = current_setting('app.tenant', true));
grant select on accounts to s1_reader;
alter default privileges grant select on tables to s1_reader;
alter default privileges in schema public grant usage on sequences to s1_reader;
create table after_dp(id int);
insert into accounts(tenant,name) values ('t1','a'),('t2','b');
SQL
adm postgres -c "alter database s1_tpl_build allow_connections false" \
             -c "select count(pg_terminate_backend(pid)) from pg_stat_activity where datname='s1_tpl_build' and pid<>pg_backend_pid()" \
             -c "alter database s1_tpl_build rename to s1_tpl" -c "alter database s1_tpl is_template true"
# clone role
adm postgres -c "create role s1_wt_a login password 'rolepw'"
if [ "$MAJOR" -ge 16 ]; then adm postgres -c "grant s1_wt_a to s1admin with inherit true, set true"; else adm postgres -c "grant s1_wt_a to s1admin"; fi
t0=$(date +%s%N)
adm postgres -c "create database s1_wt_a template s1_tpl owner s1_wt_a"
echo "   clone ms: $(( ($(date +%s%N)-t0)/1000000 ))"
echo "-- guard (expect 0):"; adm s1_wt_a -Atc "select (select count(*) from pg_database where datdba='s1_wt_owner'::regrole)+(select count(*) from pg_tablespace where spcowner='s1_wt_owner'::regrole)"
echo "-- REASSIGN without schema pre-step (expect permission denied for schema public as non-superuser):"
adm s1_wt_a -c "begin" -c "reassign owned by s1_wt_owner to s1_wt_a" -c "rollback" 2>&1 | sed 's/^/   /'
echo "-- fix: ALTER SCHEMA owned by owner -> wt role first, then REASSIGN"
adm s1_wt_a -Atc "select format('ALTER SCHEMA %I OWNER TO s1_wt_a;', nspname) from pg_namespace where nspowner='s1_wt_owner'::regrole" | adm s1_wt_a
adm s1_wt_a -c "reassign owned by s1_wt_owner to s1_wt_a" && echo "   OK reassign"
echo "-- owners after reassign:"; adm s1_wt_a -Atc "select string_agg(distinct pg_get_userbyid(relowner),',') from pg_class where relnamespace='public'::regnamespace" -c "select pg_get_userbyid(nspowner) from pg_namespace where nspname='public'" -c "select pg_get_userbyid(proowner) from pg_proc where proname='f_one'" -c "select pg_get_userbyid(typowner) from pg_type where typname='mood'"
echo "-- pg_default_acl after reassign (defaclrole):"; adm s1_wt_a -Atc "select pg_get_userbyid(defaclrole), defaclobjtype, coalesce(defaclnamespace::regnamespace::text,'-'), defaclacl from pg_default_acl"
# replay default ACLs for the worktree role
adm s1_wt_a -Atc "select format('ALTER DEFAULT PRIVILEGES FOR ROLE s1_wt_a %s GRANT %s ON %s TO %s', case when d.defaclnamespace=0 then '' else 'IN SCHEMA '||quote_ident(d.defaclnamespace::regnamespace::text) end, a.privilege_type, case d.defaclobjtype when 'r' then 'TABLES' when 'S' then 'SEQUENCES' when 'f' then 'FUNCTIONS' when 'T' then 'TYPES' when 'n' then 'SCHEMAS' end, case when a.grantee=0 then 'PUBLIC' else quote_ident(pg_get_userbyid(a.grantee)) end)||';' from pg_default_acl d, aclexplode(d.defaclacl) a where d.defaclrole='s1_wt_owner'::regrole" > /tmp/s1_dacl_$PORT.sql
grep -v 'TO s1_wt_owner;$' /tmp/s1_dacl_$PORT.sql | adm s1_wt_a
adm postgres -c "revoke connect on database s1_wt_a from public" -c "grant connect on database s1_wt_a to s1_wt_a"
echo "-- migrate as worktree role (ALTER existing + new table inherits replayed default privs):"
as s1_wt_a s1_wt_a -c "alter table accounts add column email text" -c "create table new_t(id int)" -c "drop policy tenant_iso on accounts" -c "create policy tenant_iso on accounts using (tenant = current_setting('app.tenant', true))" && echo "   OK migrate"
adm s1_wt_a -Atc "select has_table_privilege('s1_reader','new_t','SELECT') as reader_new_t, has_table_privilege('s1_reader','after_dp','SELECT') as reader_after_dp"
echo "-- RLS for reader (non-owner) sees tenant rows only:"; adm s1_wt_a -c "grant s1_reader to s1admin" >/dev/null 2>&1; adm s1_wt_a -Atc "set role s1_reader; set app.tenant='t1'; select count(*) from accounts"
echo "-- template datdba + flags:"; adm postgres -Atc "select pg_get_userbyid(datdba), datistemplate, datallowconn from pg_database where datname='s1_tpl'"
echo "-- PUBLIC connect to clone revoked:"; adm postgres -Atc "select has_database_privilege('s1_reader','s1_wt_a','CONNECT')"
echo "-- drop clone + role:"; adm postgres -c "drop database s1_wt_a with (force)" -c "drop role s1_wt_a" && echo "   OK teardown"
