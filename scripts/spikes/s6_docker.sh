#!/usr/bin/env bash
# Spike S6: image VOLUME paths per major, POSTGRES_PASSWORD via inherited env (not argv),
# readiness: unix-socket pg_isready inside container vs TCP query from host during init restart.
# Usage: s6_docker.sh <image> <host-port>
set -u
IMG=$1; PORT=$2; NAME=wtpg-test-s6
echo "== $IMG volumes: $(docker image inspect "$IMG" --format '{{json .Config.Volumes}}') PGDATA: $(docker image inspect "$IMG" --format '{{range .Config.Env}}{{println .}}{{end}}' | grep PGDATA)"
docker rm -f $NAME >/dev/null 2>&1
export POSTGRES_PASSWORD=s6secret
docker run -d --name $NAME --label worktree-pg=spike -e POSTGRES_PASSWORD -p 127.0.0.1:$PORT:5432 "$IMG" >/dev/null &
RUNPID=$!; sleep 0.05
echo "   docker CLI argv contains secret: $(tr '\0' ' ' < /proc/$RUNPID/cmdline 2>/dev/null | grep -c s6secret)"
wait $RUNPID
echo "   docker inspect Config.Env exposes secret: $(docker inspect $NAME --format '{{json .Config.Env}}' | grep -c s6secret)"
t0=$(date +%s%N); sock=""; tcp=""
for i in $(seq 1 300); do
  now=$(( ($(date +%s%N)-t0)/1000000 ))
  [ -z "$sock" ] && docker exec $NAME pg_isready -q -h /var/run/postgresql 2>/dev/null && sock=$now
  [ -z "$tcp" ] && PGPASSWORD=s6secret psql -X -h 127.0.0.1 -p $PORT -U postgres -Atc 'select 1' postgres >/dev/null 2>&1 && tcp=$now
  [ -n "$sock" ] && [ -n "$tcp" ] && break
  sleep 0.1
done
echo "   first ready: unix-socket pg_isready=${sock}ms  host TCP select1=${tcp}ms"
echo "   'ready to accept connections' log lines: $(docker logs $NAME 2>&1 | grep -c 'ready to accept connections')"
echo "   'PostgreSQL init process complete' seen: $(docker logs $NAME 2>&1 | grep -c 'init process complete')"
echo "   mounts: $(docker inspect $NAME --format '{{range .Mounts}}{{.Type}}:{{.Destination}} {{end}}')"
docker rm -f -v $NAME >/dev/null
