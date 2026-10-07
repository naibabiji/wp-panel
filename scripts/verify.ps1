$ErrorActionPreference = 'Stop'

$env:GOCACHE = Join-Path (Get-Location) '.gocache'
$packages = go list ./... | Where-Object { $_ -notlike '*/scratch' }

go test $packages
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

go vet $packages
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

go build -o (Join-Path $env:GOCACHE 'wp-panel-verify.exe') .
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

git diff --check
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

# Private development repository only; skipped where private-tools/ is absent.
if (Test-Path 'private-tools/verify-capability-map.py') {
    $python = if (Get-Command python3 -ErrorAction SilentlyContinue) { 'python3' } else { 'python' }
    & $python private-tools/verify-capability-map.py --generate
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    & $python -m unittest private-tools/test_verify_capability_map.py private-tools/test_check_public_ai_live.py
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
}
exit 0
