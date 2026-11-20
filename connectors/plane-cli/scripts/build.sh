#!/usr/bin/env bash
set -euo pipefail

# Build plane-cli for the host platform.
# Usage: scripts/build.sh [version] [output]
#   version — embedded as -X main.version=<version>; default "dev"
#   output  — the program path; default build/plane-cli
# The catalog workflow runs this on one runner for each platform.
# It also writes plane-op next to the program. The Trebi operations run as
# plane-op <name>, because an operation program must not also be a run
# command. It is a copy, not a link: Trebi installs only regular files.

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION="${1:-dev}"
OUTPUT="${2:-${ROOT_DIR}/build/plane-cli}"
case "${OUTPUT}" in /*) ;; *) OUTPUT="${PWD}/${OUTPUT}" ;; esac

mkdir -p "$(dirname "${OUTPUT}")"
cd "${ROOT_DIR}"
echo "Building plane-cli ${VERSION} → ${OUTPUT}"
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o "${OUTPUT}" .
rm -f "$(dirname "${OUTPUT}")/plane-op"
cp "${OUTPUT}" "$(dirname "${OUTPUT}")/plane-op"
