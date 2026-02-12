#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

GO_BIN="${GO_BIN:-}"
if [[ -z "$GO_BIN" ]]; then
  if command -v go >/dev/null 2>&1; then
    GO_BIN="$(command -v go)"
  elif [[ -x /usr/local/go/bin/go ]]; then
    GO_BIN="/usr/local/go/bin/go"
  else
    echo "[ERR] go binary not found" >&2
    exit 1
  fi
fi

required_files=(
  "docs/refactor/phase5-plan.md"
  "docs/refactor/phase5-slo.md"
  "docs/refactor/phase5-test-report.md"
  "docs/refactor/phase5-capacity-model.md"
  "docs/refactor/phase5-rollback.md"
  "docs/refactor/phase5-go-live-report.md"
  "docs/runbooks/phase5-incident-runbook.md"
  "docs/runbooks/alerts-routing.md"
  "deploy/monitoring/phase5-alert-rules.yaml"
)

echo "[INFO] Checking Phase 5 deliverables..."
for f in "${required_files[@]}"; do
  if [[ ! -f "$f" ]]; then
    echo "[ERR] missing: $f" >&2
    exit 1
  fi
  echo "[OK] $f"
done

echo "[INFO] Running tests..."
"$GO_BIN" test ./...

if [[ "${RUN_CAPACITY_BENCH:-false}" == "true" ]]; then
  echo "[INFO] Running capacity benchmark..."
  "$ROOT_DIR/bin/phase5-capacity-bench.sh"
fi

echo "[DONE] Phase 5 baseline checks passed"
