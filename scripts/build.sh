#!/usr/bin/env bash
set -euo pipefail

# Build the discord-cli (discord-cli) binary into ./build/.
# Usage: scripts/build.sh [version]
#   version — optional, embedded into the binary as -X main.version=<version>

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
BUILD_DIR="${ROOT_DIR}/build"
OUTPUT="${BUILD_DIR}/discord-cli"

VERSION="${1:-${DISCORD_CLI_VERSION:-$(git -C "${ROOT_DIR}" describe --tags --always --dirty 2>/dev/null || echo dev)}}"

mkdir -p "${BUILD_DIR}"

cd "${ROOT_DIR}"
echo "Building discord-cli ${VERSION} → ${OUTPUT}"
go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o "${OUTPUT}" .
echo "Done."
