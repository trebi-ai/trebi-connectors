#!/usr/bin/env bash
set -euo pipefail

# Build whatsapp-cli for the host platform. It needs CGO (go-sqlite3) and
# the sqlite_fts5 build tag for search.
# Usage: scripts/build.sh [version] [output]
#   version — embedded as -X main.version=<version>; default "dev"
#   output  — the program path; default build/whatsapp-cli
# The catalog workflow runs this on one runner for each platform.

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION="${1:-dev}"
OUTPUT="${2:-${ROOT_DIR}/build/whatsapp-cli}"
case "${OUTPUT}" in /*) ;; *) OUTPUT="${PWD}/${OUTPUT}" ;; esac

mkdir -p "$(dirname "${OUTPUT}")"
cd "${ROOT_DIR}"
echo "Building whatsapp-cli ${VERSION} → ${OUTPUT}"
CGO_ENABLED=1 go build -trimpath -tags sqlite_fts5 -ldflags "-s -w -X main.version=${VERSION}" -o "${OUTPUT}" ./cmd/whatsapp-cli
