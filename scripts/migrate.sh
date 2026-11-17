#!/usr/bin/env bash
# Minimal migration runner.
#
# Applies every migrations/*.sql exactly once, in filename order, and records
# which ones ran in a schema_migrations table. Re-running is a no-op.
#
# It talks to Postgres with `kubectl exec` into the primary pod instead of a
# port-forward, so there is no second terminal tab and no port to collide.
set -euo pipefail

NS=${NS:-ledgerline}
DB=${DB:-ledgerline}
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# Ask CNPG which pod is currently the primary rather than hardcoding pg-1 --
# after a failover the primary may well be pg-2.
primary=$(kubectl -n "$NS" get pods \
  -l 'cnpg.io/cluster=pg,role=primary' \
  -o jsonpath='{.items[0].metadata.name}')
[[ -n "$primary" ]] || { echo "no primary pod found for cluster pg" >&2; exit 1; }
echo "primary is $primary"

# psql flags used everywhere below:
#   -v ON_ERROR_STOP=1  make psql exit non-zero on the first SQL error.
#                       Without it psql reports the error and still exits 0,
#                       so a broken migration would look like it succeeded.
#   -qtA                quiet, tuples only, unaligned -- output is a bare value.
psql_run() { kubectl -n "$NS" exec -i "$primary" -c postgres -- \
  psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 "$@"; }

psql_run -qtA -c "
  CREATE TABLE IF NOT EXISTS schema_migrations (
    version     text PRIMARY KEY,
    applied_at  timestamptz NOT NULL DEFAULT now()
  );" >/dev/null

shopt -s nullglob
for file in "$REPO_ROOT"/migrations/*.sql; do
  version=$(basename "$file")

  applied=$(psql_run -qtA -c \
    "SELECT 1 FROM schema_migrations WHERE version = '$version';")
  if [[ "$applied" == "1" ]]; then
    echo "  skip    $version"
    continue
  fi

  echo "  apply   $version"
  # BEGIN/COMMIT around the file and the bookkeeping row together: if the
  # migration fails halfway, the row is not written and nothing is left behind.
  # Postgres does transactional DDL, which is exactly why this works.
  { echo "BEGIN;"
    cat "$file"
    echo ";"
    echo "INSERT INTO schema_migrations (version) VALUES ('$version');"
    echo "COMMIT;"
  } | psql_run -qtA -f - >/dev/null

  # The tables are created by the postgres superuser, but the app connects as
  # 'app'. Without this grant every query from the service fails on permissions.
  psql_run -qtA -c "
    GRANT ALL ON ALL TABLES IN SCHEMA public TO app;
    GRANT ALL ON ALL SEQUENCES IN SCHEMA public TO app;" >/dev/null
done

echo "migrations up to date"
psql_run -qtA -c "SELECT version FROM schema_migrations ORDER BY version;" \
  | sed 's/^/  applied: /'
