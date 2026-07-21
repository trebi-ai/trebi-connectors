#!/usr/bin/env bash
set -euo pipefail

# Build the whatsapp-cli binary into ./build/.
# Requires CGO (go-sqlite3) and the sqlite_fts5 build tag for FTS search.
# Usage: scripts/build.sh [version]
#   version — optional, embedded into the binary as -X main.version=<version>

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
BUILD_DIR="${ROOT_DIR}/build"
OUTPUT="${BUILD_DIR}/whatsapp-cli"

VERSION="${1:-${WHATSAPP_CLI_VERSION:-$(git -C "${ROOT_DIR}" describe --tags --always --dirty 2>/dev/null || echo dev)}}"

mkdir -p "${BUILD_DIR}"

cd "${ROOT_DIR}"
echo "Building whatsapp-cli ${VERSION} → ${OUTPUT}"
CGO_ENABLED=1 go build -trimpath -tags sqlite_fts5 -ldflags "-s -w -X main.version=${VERSION}" -o "${OUTPUT}" ./cmd/whatsapp-cli
echo "Done."
