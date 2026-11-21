#!/usr/bin/env bash
# Build one connector program for the host platform.
# Usage: scripts/go-build.sh <cli-dir> [version] [output]
#   cli-dir — the folder name in connectors/
#   version — embedded as -X main.version=<version>; default "dev"
#   output  — the program path; default connectors/<cli-dir>/build/<cli-dir>
# The catalog workflow runs this on one runner for each platform.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CLI="${1:-}"
if [ -z "$CLI" ] || [ ! -f "${ROOT_DIR}/connectors/${CLI}/go.mod" ]; then
  echo "usage: go-build.sh <cli-dir> [version] [output]" >&2
  exit 2
fi
VERSION="${2:-dev}"
OUTPUT="${3:-${ROOT_DIR}/connectors/${CLI}/build/${CLI}}"
case "${OUTPUT}" in /*) ;; *) OUTPUT="${PWD}/${OUTPUT}" ;; esac

mkdir -p "$(dirname "${OUTPUT}")"
cd "${ROOT_DIR}/connectors/${CLI}"
echo "Building ${CLI} ${VERSION} → ${OUTPUT}"
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o "${OUTPUT}" .
