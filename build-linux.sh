#!/usr/bin/env bash
# Builds build/jiraffe-linux-amd64: the Linux (Ubuntu 22.04+) front end with
# GTK 3 and WebKitGTK 4.1 (cgo). Needs:
#   sudo apt install build-essential pkg-config libwebkit2gtk-4.1-dev
#
#   ./build-linux.sh            build
#   ./build-linux.sh --check    also run go vet and go test with cgo first
#
# Version as in build.sh ($VERSION, else the latest vX.Y.Z tag).
set -euo pipefail

cd "$(dirname "$0")"
export PATH="$PATH:/usr/local/go/bin"
export CGO_ENABLED=1

if [[ -z "${VERSION:-}" ]]; then
  VERSION="$(git describe --tags --abbrev=0 --match 'v[0-9]*' 2>/dev/null || echo v0.0.0)"
  VERSION="${VERSION#v}"
fi
if [[ ! "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "ERROR: VERSION must be X.Y.Z, got '$VERSION'" >&2
  exit 1
fi
# The updater downloads at most 20 MiB (update.MaxDownload).
MAX_BYTES=$((20 * 1024 * 1024))

if ! "${PKG_CONFIG:-pkg-config}" --exists gtk+-3.0 webkit2gtk-4.1; then
  echo "ERROR: GTK 3 / WebKitGTK 4.1 headers not found: sudo apt install pkg-config libwebkit2gtk-4.1-dev" >&2
  exit 1
fi

echo "==> icon"
# assets/icon.png is generated (not in git); main_linux.go embeds it.
go run ./tools/icongen -out assets

echo "==> ui bundle"
mkdir -p build
go run ./tools/uibundle -src ui -out build/ui.bundle.gz

if [[ "${1:-}" == "--check" ]]; then
  echo "==> go vet / go test (linux, cgo)"
  go vet ./...
  go vet -tags uibundle .
  go vet -tags snapshotdebug .
  go test ./...
fi

echo "==> build $VERSION"
go build -trimpath -tags uibundle \
  -ldflags "-s -w -X jiraffe/internal/app.Version=$VERSION" \
  -o build/jiraffe-linux-amd64 .

head -c 4 build/jiraffe-linux-amd64 | grep -q 'ELF' || { echo "ERROR: not an ELF binary" >&2; exit 1; }
size=$(stat -c %s build/jiraffe-linux-amd64)
awk -v s="$size" 'BEGIN { printf "==> build/jiraffe-linux-amd64: %d bytes (%.2f MB)\n", s, s / 1048576 }'
if (( size > MAX_BYTES )); then
  echo "ERROR: binary exceeds the 20 MiB update limit" >&2
  exit 1
fi
