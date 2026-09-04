#!/usr/bin/env bash
set -euo pipefail

if [[ -z "${DATABASE_URL:-}" ]]; then
  echo "DATABASE_URL is required" >&2
  exit 1
fi
if [[ -z "${METRICS_AUTH_TOKEN:-}" ]]; then
  echo "METRICS_AUTH_TOKEN is required" >&2
  exit 1
fi

profile_dir="${1:-/tmp/tellbook-scale-baseline}"
config_path="${profile_dir}/target.json"
if [[ ! -f "$config_path" ]]; then
  echo "Missing generated target config: $config_path" >&2
  exit 1
fi

api_origin="${SCALE_API_ORIGIN:-http://127.0.0.1:8200}"
api_origin="${api_origin%/}"
artifact_dir="${2:-artifacts/performance/$(date -u +%Y%m%dT%H%M%SZ)}"
mkdir -p "$artifact_dir"
scenario_config="$artifact_dir/scenario-config.json"
node -e '
  const fs = require("node:fs");
  const [source, target, origin] = process.argv.slice(1);
  const config = JSON.parse(fs.readFileSync(source, "utf8"));
  config.base_url = `${origin}/v1`;
  fs.writeFileSync(target, `${JSON.stringify(config, null, 2)}\n`, { mode: 0o600 });
' "$config_path" "$scenario_config" "$api_origin"

curl -fsS "${api_origin}/v1/readyz" > "$artifact_dir/readiness.json"
curl -fsS -H "Authorization: Bearer ${METRICS_AUTH_TOKEN}" \
  "${api_origin}/internal/metrics" > "$artifact_dir/metrics-before.prom"

load_flags=()
if [[ "$api_origin" == http://127.0.0.1:* || "$api_origin" == http://localhost:* ]]; then
  load_flags+=(--allow-http)
fi
node scripts/inbox-load.mjs \
  --config "$scenario_config" \
  --confirm-write \
  --out "$artifact_dir/api-sse-report.json" \
  "${load_flags[@]}"

curl -fsS -H "Authorization: Bearer ${METRICS_AUTH_TOKEN}" \
  "${api_origin}/internal/metrics" > "$artifact_dir/metrics-after.prom"
bash scripts/capture-scale-baseline.sh "$artifact_dir"

echo "$artifact_dir"
