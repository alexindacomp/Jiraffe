#!/usr/bin/env bash
# Cross-compiles build/jiraffe.exe from WSL/Linux (no CGO), with the app icon,
# manifest (PerMonitorV2 DPI awareness, common controls v6) and version info.
#
#   ./build.sh            build
#   ./build.sh --check    also run go vet and go test (Linux) first
#
# The release binary embeds the UI as one compressed bundle
# (build/ui.bundle.gz from tools/uibundle, -tags uibundle); plain
# `go build` / `go test` embed the raw ui/ files instead (ui_raw.go).
set -euo pipefail

cd "$(dirname "$0")"
export PATH="$PATH:/usr/local/go/bin"

# Version: $VERSION (CI passes the release tag), else the latest vX.Y.Z git tag.
if [[ -z "${VERSION:-}" ]]; then
  VERSION="$(git describe --tags --abbrev=0 --match 'v[0-9]*' 2>/dev/null || echo v0.0.0)"
  VERSION="${VERSION#v}"
fi
if [[ ! "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "ERROR: VERSION must be X.Y.Z, got '$VERSION'" >&2
  exit 1
fi
WINRES="github.com/tc-hib/go-winres@v0.3.3"
MAX_BYTES=$((5 * 1024 * 1024))

# The UI is developed separately; make sure the embed target exists.
if [[ ! -f ui/index.html ]]; then
  echo "==> ui/index.html missing, writing a placeholder"
  mkdir -p ui
  printf '<!doctype html><meta charset="utf-8"><title>Jiraffe</title><p>UI is not built yet.</p>\n' > ui/index.html
fi

echo "==> ui bundle"
mkdir -p build
go run ./tools/uibundle -src ui -out build/ui.bundle.gz

if [[ "${1:-}" == "--check" ]]; then
  echo "==> go vet / go test (linux)"
  go vet ./...
  GOOS=windows go vet ./...
  GOOS=windows go vet -tags uibundle ./...
  go test ./...
  go test -tags uibundle .
fi

echo "==> icon"
go run ./tools/icongen -out assets

echo "==> resources (.syso)"
# go run pkg@version builds the tool outside this module: nothing is added to
# go.mod and nothing ends up in the binary except the generated resources.
go run "$WINRES" make --in assets/winres.json --arch amd64 --out rsrc \
  --file-version "$VERSION.0" --product-version "$VERSION.0"

echo "==> build $VERSION"
mkdir -p build
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -tags uibundle \
  -ldflags "-s -w -H windowsgui -X jiraffe/internal/app.Version=$VERSION" \
  -o build/jiraffe.exe .

size=$(stat -c %s build/jiraffe.exe)
awk -v s="$size" 'BEGIN { printf "==> build/jiraffe.exe: %d bytes (%.2f MB)\n", s, s / 1048576 }'
if (( size > MAX_BYTES )); then
  echo "ERROR: binary exceeds the 5 MB budget" >&2
  exit 1
fi
