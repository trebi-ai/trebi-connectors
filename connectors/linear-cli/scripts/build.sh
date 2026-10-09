#!/usr/bin/env bash
set -euo pipefail

# Build linear-cli for the host platform.
# Usage: scripts/build.sh [version] [output]
#   version — embedded as -X main.version=<version>; default "dev"
#   output  — the program path; default build/linear-cli
# The catalog workflow runs this on one runner for each platform.

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION="${1:-dev}"
OUTPUT="${2:-${ROOT_DIR}/build/linear-cli}"
case "${OUTPUT}" in /*) ;; *) OUTPUT="${PWD}/${OUTPUT}" ;; esac

mkdir -p "$(dirname "${OUTPUT}")"
cd "${ROOT_DIR}"
echo "Building linear-cli ${VERSION} → ${OUTPUT}"
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o "${OUTPUT}" .
