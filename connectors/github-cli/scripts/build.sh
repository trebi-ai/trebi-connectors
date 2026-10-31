#!/usr/bin/env bash
set -euo pipefail

# Build github-cli for GOOS/GOARCH (default: the host).
# Usage: scripts/build.sh [version] [output]
#   version — embedded as -X main.version=<version>; default "dev"
#   output  — the program path; default build/github-cli
#   TREBI_GITHUB_CLIENT_ID — the OAuth app of the device login; empty needs a token
#
# The output folder gets five files. The catalog archive holds each of them.
#   github-cli  the program
#   github-op   a copy, for the Trebi operations (an operation program must not also be a run command)
#   gh          a copy; with this name the program runs the gh of the connection
#   gh-bin      the real gh, pinned to GH_VERSION and checked with SHA-256
#   gh-LICENSE  the licence of gh (MIT)
# The copies are files, not links: Trebi installs only regular files.

GH_VERSION=2.102.0
gh_sha256() {
  case "$1" in
    linux_amd64) echo bb766f710eef8ede859c18578c72c327597cd4c8a85b06001b1f3843c6019386 ;;
    linux_arm64) echo 7862c86c72f43df3a2d93ddde6f473285b4e2af61b494849846827e513ef6484 ;;
    macOS_amd64) echo b245f24eb2bf5f75b426b4c26da3651a107f8d5b6f4fddfbfccc5679041378b3 ;;
    macOS_arm64) echo da922c20d1792e5b2cbf375593d7a658acf034c12c84e007e71c76ef959c337e ;;
    *) echo "no gh for $1" >&2; return 1 ;;
  esac
}

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION="${1:-dev}"
OUTPUT="${2:-${ROOT_DIR}/build/github-cli}"
case "${OUTPUT}" in /*) ;; *) OUTPUT="${PWD}/${OUTPUT}" ;; esac
DIR="$(dirname "${OUTPUT}")"

mkdir -p "${DIR}"
cd "${ROOT_DIR}"
GOOS="$(go env GOOS)"
GOARCH="$(go env GOARCH)"
echo "Building github-cli ${VERSION} for ${GOOS}/${GOARCH} → ${OUTPUT}"
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION} -X main.clientID=${TREBI_GITHUB_CLIENT_ID:-}" -o "${OUTPUT}" .
for name in github-op gh; do
  rm -f "${DIR:?}/${name}"
  cp "${OUTPUT}" "${DIR}/${name}"
done

# The real gh.
case "${GOOS}" in
  darwin) asset="gh_${GH_VERSION}_macOS_${GOARCH}.zip"; key="macOS_${GOARCH}" ;;
  linux) asset="gh_${GH_VERSION}_linux_${GOARCH}.tar.gz"; key="linux_${GOARCH}" ;;
  *) echo "no gh for ${GOOS}" >&2; exit 1 ;;
esac
want="$(gh_sha256 "${key}")"
cache="${RUNNER_TOOL_CACHE:-${ROOT_DIR}/build/cache}/gh/${GH_VERSION}"
mkdir -p "${cache}"
if [ ! -f "${cache}/${asset}" ]; then
  echo "Downloading ${asset}"
  curl -fsSL --retry 3 -o "${cache}/${asset}.part" "https://github.com/cli/cli/releases/download/v${GH_VERSION}/${asset}"
  mv "${cache}/${asset}.part" "${cache}/${asset}"
fi
if command -v sha256sum >/dev/null; then
  got="$(sha256sum "${cache}/${asset}" | cut -d' ' -f1)"
else
  got="$(shasum -a 256 "${cache}/${asset}" | cut -d' ' -f1)"
fi
if [ "${got}" != "${want}" ]; then
  rm -f "${cache:?}/${asset}"
  echo "SHA-256 of ${asset} is ${got}, not ${want}" >&2
  exit 1
fi
tmp="$(mktemp -d)"
trap 'rm -rf "${tmp:?}"' EXIT
case "${asset}" in
  *.zip) unzip -q "${cache}/${asset}" -d "${tmp}" ;;
  *) tar -xzf "${cache}/${asset}" -C "${tmp}" ;;
esac
src="${tmp}/${asset%.zip}"
src="${src%.tar.gz}"
rm -f "${DIR:?}/gh-bin" "${DIR:?}/gh-LICENSE"
cp "${src}/bin/gh" "${DIR}/gh-bin"
cp "${src}/LICENSE" "${DIR}/gh-LICENSE"
chmod 0755 "${DIR}/gh-bin"

# Run the gh copy when this host can run the target. The Mac runs amd64
# through Rosetta.
host="$(go env GOHOSTOS)/$(go env GOHOSTARCH)"
if [ "${GOOS}/${GOARCH}" = "${host}" ] || { [ "${GOOS}/${GOARCH}" = darwin/amd64 ] && [ "${host}" = darwin/arm64 ]; }; then
  env -u TREBI_STATE_DIR "${DIR}/gh" --version | grep -F "gh version ${GH_VERSION}"
fi
