#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
BUILD_DIR="${ROOT_DIR}/build"
OUTPUT="${BUILD_DIR}/slack-cli"

VERSION="${SLACK_CLI_VERSION:-$(git -C "${ROOT_DIR}" describe --tags --always --dirty 2>/dev/null || echo dev)}"

mkdir -p "${BUILD_DIR}"

cd "${ROOT_DIR}"
echo "Building slack-cli ${VERSION} → ${OUTPUT}"
go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o "${OUTPUT}" .
# The Trebi operations run as slack-op <name>.
ln -sf slack-cli "${BUILD_DIR}/slack-op"
echo "Done."
