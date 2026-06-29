#!/usr/bin/env bash
set -euo pipefail

export GOCACHE="${GOCACHE:-${TMPDIR:-/tmp}/wp-panel-go-build-cache}"
mkdir -p "$GOCACHE"

go test ./...
go vet ./...
go build -o "${TMPDIR:-/tmp}/wp-panel-verify" .
git diff --check

if command -v php >/dev/null 2>&1; then
  php -l wp-panel-optimizer/wp-panel-optimizer.php
else
  echo "php not found; skipped php -l wp-panel-optimizer/wp-panel-optimizer.php"
fi
