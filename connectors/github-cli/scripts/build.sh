#!/usr/bin/env bash
set -euo pipefail

# Build github-cli for the host platform.
# Usage: scripts/build.sh [version] [output]
#   version — embedded as -X main.version=<version>; default "dev"
#   output  — the program path; default build/github-cli
#   TREBI_GITHUB_CLIENT_ID — the OAuth app of the device login; empty needs a token

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION="${1:-dev}"
OUTPUT="${2:-${ROOT_DIR}/build/github-cli}"
case "${OUTPUT}" in /*) ;; *) OUTPUT="${PWD}/${OUTPUT}" ;; esac

mkdir -p "$(dirname "${OUTPUT}")"
cd "${ROOT_DIR}"
echo "Building github-cli ${VERSION} → ${OUTPUT}"
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION} -X main.clientID=${TREBI_GITHUB_CLIENT_ID:-}" -o "${OUTPUT}" .
