#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${1:-http://127.0.0.1:9999}"
MAX_CLEANUP_ERROR="${MAX_CLEANUP_ERROR:-5}"
MAX_PROCESSING="${MAX_PROCESSING:-50}"
MAX_PENDING="${MAX_PENDING:-500}"

extract_number() {
  local json="$1"
  local key="$2"
  echo "$json" | sed -n "s/.*\"${key}\"[[:space:]]*:[[:space:]]*\([0-9][0-9]*\).*/\1/p" | head -n1
}

extract_bool() {
  local json="$1"
  local key="$2"
  echo "$json" | sed -n "s/.*\"${key}\"[[:space:]]*:[[:space:]]*\(true\|false\).*/\1/p" | head -n1
}

extract_string() {
  local json="$1"
  local key="$2"
  echo "$json" | sed -n "s/.*\"${key}\"[[:space:]]*:[[:space:]]*\"\([^\"]*\)\".*/\1/p" | head -n1
}

echo "[INFO] Phase 5 canary gate check: ${BASE_URL}"

health_json="$(curl -fsS "${BASE_URL}/api/health")"
status="$(extract_string "$health_json" status)"
db_ok="$(extract_bool "$health_json" database)"
worker_ok="$(extract_bool "$health_json" worker)"

if [[ "$status" != "healthy" || "$db_ok" != "true" || "$worker_ok" != "true" ]]; then
  echo "[FAIL] health check failed: ${health_json}" >&2
  exit 1
fi

echo "[OK] health check passed"

stats_json="$(curl -fsS "${BASE_URL}/api/stats")"
pending="$(extract_number "$stats_json" pending)"
processing="$(extract_number "$stats_json" processing)"
cleanup_error="$(extract_number "$stats_json" cleanup_error)"

pending="${pending:-0}"
processing="${processing:-0}"
cleanup_error="${cleanup_error:-0}"

echo "[INFO] stats: pending=${pending}, processing=${processing}, cleanup_error=${cleanup_error}"

if (( cleanup_error > MAX_CLEANUP_ERROR )); then
  echo "[FAIL] cleanup_error=${cleanup_error} exceeds threshold ${MAX_CLEANUP_ERROR}" >&2
  exit 1
fi

if (( processing > MAX_PROCESSING )); then
  echo "[FAIL] processing=${processing} exceeds threshold ${MAX_PROCESSING}" >&2
  exit 1
fi

if (( pending > MAX_PENDING )); then
  echo "[FAIL] pending=${pending} exceeds threshold ${MAX_PENDING}" >&2
  exit 1
fi

echo "[PASS] canary gate passed"
