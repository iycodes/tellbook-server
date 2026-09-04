#!/usr/bin/env bash
set -euo pipefail

if [[ -z "${DATABASE_URL:-}" ]]; then
  echo "DATABASE_URL is required" >&2
  exit 1
fi

artifact_dir="${1:-artifacts/performance/$(date -u +%Y%m%dT%H%M%SZ)}"
mkdir -p "$artifact_dir"

psql "$DATABASE_URL" -v ON_ERROR_STOP=1 \
  -o "$artifact_dir/query-plans.txt" \
  -f scripts/scale-query-plans.sql

psql "$DATABASE_URL" -v ON_ERROR_STOP=1 \
  -o "$artifact_dir/top-sql.txt" \
  -c "SELECT queryid, calls, total_exec_time, mean_exec_time, rows, LEFT(query, 240) AS query FROM pg_stat_statements ORDER BY total_exec_time DESC LIMIT 30;"

{
  printf 'captured_at_utc=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  printf 'go_version=%s\n' "$(go version)"
  printf 'database_version='
  psql "$DATABASE_URL" -Atc "SHOW server_version"
  printf 'database_size_bytes='
  psql "$DATABASE_URL" -Atc "SELECT pg_database_size(current_database())"
} > "$artifact_dir/environment.txt"

echo "$artifact_dir"
