#!/usr/bin/env bash
set -euo pipefail

export GOCACHE="${GOCACHE:-${TMPDIR:-/tmp}/wp-panel-go-build-cache}"
mkdir -p "$GOCACHE"

go test ./...
go vet ./...
go build -o "${TMPDIR:-/tmp}/wp-panel-verify" .
git diff --check

# Private development repository only; skipped where private-tools/ is absent.
if [ -f private-tools/verify-capability-map.py ]; then
  python3 private-tools/verify-capability-map.py --generate
  python3 -m unittest private-tools/test_verify_capability_map.py private-tools/test_check_public_ai_live.py
fi

if command -v php >/dev/null 2>&1; then
  php -l wp-panel-optimizer/wp-panel-optimizer.php
else
  echo "php not found; skipped php -l wp-panel-optimizer/wp-panel-optimizer.php"
fi
