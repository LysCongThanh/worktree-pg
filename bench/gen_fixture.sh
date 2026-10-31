#!/usr/bin/env bash
# Generate the benchmark fixture: a deterministic, synthetic "medium app" schema
# written as plain SQL migrations (no external download, no license questions).
#   bench/gen_fixture.sh <out-dir> [tables=60] [rows=5000]
# Each migration NNN_<table>.sql creates one table (10 columns, FK to the
# previous table, 2 indexes, a view and an updated_at trigger) and seeds `rows`
# rows server-side with generate_series. Same arguments => byte-identical files.
set -euo pipefail
out=${1:?usage: gen_fixture.sh <out-dir> [tables] [rows]}
tables=${2:-60}
rows=${3:-5000}
mkdir -p "$out/db/migrations"
cat > "$out/db/migrations/000_base.sql" <<'SQL'
CREATE FUNCTION touch_updated_at() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN NEW.updated_at := now(); RETURN NEW; END $$;
SQL
for i in $(seq 1 "$tables"); do
  t=$(printf 't%03d' "$i")
  fk=""
  [ "$i" -gt 1 ] && fk="parent_id bigint REFERENCES $(printf 't%03d' $((i - 1)))(id),"
  parent_val="NULL"
  [ "$i" -gt 1 ] && parent_val="1 + (g % $rows)"
  [ "$i" -eq 1 ] && fk="parent_id bigint,"
  cat > "$out/db/migrations/$(printf '%03d' "$i")_$t.sql" <<SQL
CREATE TABLE $t (
  id bigserial PRIMARY KEY,
  $fk
  public_id uuid NOT NULL DEFAULT gen_random_uuid(),
  name text NOT NULL,
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active','archived')),
  amount numeric(12,2) NOT NULL DEFAULT 0,
  tags text[] NOT NULL DEFAULT '{}',
  attrs jsonb NOT NULL DEFAULT '{}',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ${t}_parent_idx ON $t(parent_id);
CREATE INDEX ${t}_status_created_idx ON $t(status, created_at DESC);
CREATE TRIGGER ${t}_touch BEFORE UPDATE ON $t FOR EACH ROW EXECUTE FUNCTION touch_updated_at();
CREATE VIEW ${t}_active AS SELECT id, name, amount FROM $t WHERE status = 'active';
INSERT INTO $t (parent_id, name, amount, tags, attrs)
SELECT $parent_val, '$t row ' || g, (g % 1000) / 10.0, ARRAY['a' || (g % 7), 'b' || (g % 11)],
       jsonb_build_object('n', g, 'even', g % 2 = 0)
FROM generate_series(1, $rows) AS g;
SQL
done
echo "fixture: $tables tables, $((tables * rows)) rows -> $out/db/migrations"
