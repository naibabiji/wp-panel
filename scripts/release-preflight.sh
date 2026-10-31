#!/usr/bin/env bash
set -Eeuo pipefail

repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
cd "$repo_root"

# Keep the scanner version explicit. Upgrade it deliberately after reviewing its
# release notes and the resulting report; do not resolve a floating @latest.
govulncheck_version="${GOVULNCHECK_VERSION:-v1.8.0}"

if [[ ! "$govulncheck_version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    printf 'FAIL: GOVULNCHECK_VERSION must be an exact semver tag, got %s\n' "$govulncheck_version" >&2
    exit 2
fi

command -v go >/dev/null || { echo 'FAIL: go is required' >&2; exit 1; }
command -v php >/dev/null || { echo 'FAIL: php is required for PHP syntax checks' >&2; exit 1; }

go_version=$(go env GOVERSION)
go_directive=$(awk '$1 == "go" { print $2; exit }' go.mod)
if [[ -z "$go_directive" ]]; then
    echo 'FAIL: go.mod has no go directive' >&2
    exit 1
fi
go_series=${go_directive%.*}
if [[ "$go_version" != go${go_series}.* ]]; then
    printf 'FAIL: Go toolchain %s is outside go.mod series %s (use the pinned project toolchain)\n' "$go_version" "$go_series" >&2
    exit 1
fi
printf 'Go toolchain: %s (go.mod series %s)\n' "$go_version" "$go_series"

scripts/verify.sh

printf 'PHP syntax: '
php_files=()
while IFS= read -r -d '' file; do
    php_files+=("$file")
done < <(find wp-panel-optimizer -type f -name '*.php' -print0 | sort -z)
if ((${#php_files[@]} == 0)); then
    echo 'no PHP files found'
    exit 1
fi
for file in "${php_files[@]}"; do
    php -l "$file" >/dev/null
done
printf '%d files OK\n' "${#php_files[@]}"

printf 'Go vulnerability scan (%s):\n' "$govulncheck_version"
go run "golang.org/x/vuln/cmd/govulncheck@${govulncheck_version}" ./...

echo 'RELEASE PREFLIGHT PASS'
