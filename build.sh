#!/usr/bin/env bash
# Cross-compiles build/jiraffe.exe from WSL/Linux (no CGO), with the app icon,
# manifest (PerMonitorV2 DPI awareness, common controls v6) and version info.
#
#   ./build.sh            build
#   ./build.sh --check    also run go vet and go test (Linux) first
set -euo pipefail

cd "$(dirname "$0")"
export PATH="$PATH:/usr/local/go/bin"

VERSION="${VERSION:-0.2.0}"
WINRES="github.com/tc-hib/go-winres@v0.3.3"
MAX_BYTES=$((5 * 1024 * 1024))

if [[ "${1:-}" == "--check" ]]; then
  echo "==> go vet / go test (linux)"
  go vet ./...
  GOOS=windows go vet ./...
  go test ./...
fi

# The UI is developed separately; make sure the embed target exists.
if [[ ! -f ui/index.html ]]; then
  echo "==> ui/index.html missing, writing a placeholder"
  mkdir -p ui
  printf '<!doctype html><meta charset="utf-8"><title>Jiraffe</title><p>UI is not built yet.</p>\n' > ui/index.html
fi

echo "==> icon"
go run ./tools/icongen -out assets

echo "==> resources (.syso)"
# go run pkg@version builds the tool outside this module: nothing is added to
# go.mod and nothing ends up in the binary except the generated resources.
go run "$WINRES" make --in assets/winres.json --arch amd64 --out rsrc \
  --file-version "$VERSION.0" --product-version "$VERSION.0"

echo "==> build"
mkdir -p build
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath \
  -ldflags "-s -w -H windowsgui -X jiraffe/internal/app.Version=$VERSION" \
  -o build/jiraffe.exe .

size=$(stat -c %s build/jiraffe.exe)
awk -v s="$size" 'BEGIN { printf "==> build/jiraffe.exe: %d bytes (%.2f MB)\n", s, s / 1048576 }'
if (( size > MAX_BYTES )); then
  echo "ERROR: binary exceeds the 5 MB budget" >&2
  exit 1
fi
