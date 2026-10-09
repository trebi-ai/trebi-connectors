#!/usr/bin/env bash
set -euo pipefail

# Build mstodo-cli for the host platform.
# Usage: scripts/build.sh [version] [output]
#   version — embedded as -X main.version=<version>; default "dev"
#   output  — the program path; default build/mstodo-cli
#   MSTODO_CLIENT_ID — the Entra public client id; when empty, the login needs --client-id or the env MSTODO_CLIENT_ID
# The catalog workflow runs this on one runner for each platform.

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION="${1:-dev}"
OUTPUT="${2:-${ROOT_DIR}/build/mstodo-cli}"
case "${OUTPUT}" in /*) ;; *) OUTPUT="${PWD}/${OUTPUT}" ;; esac

mkdir -p "$(dirname "${OUTPUT}")"
cd "${ROOT_DIR}"
echo "Building mstodo-cli ${VERSION} → ${OUTPUT}"
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION} -X main.clientID=${MSTODO_CLIENT_ID:-}" -o "${OUTPUT}" .
