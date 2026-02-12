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

echo "[INFO] Running Phase 5 capacity benchmark (ClaimPendingTasks 10k)..."
"$GO_BIN" test ./internal/database -run '^$' -bench BenchmarkClaimPendingTasks10k -benchmem
