#!/usr/bin/env bash
set -euo pipefail

# Build one CLI and link it into the bin folder.
# Usage: connectors/install.sh <cli>    for example vnc-cli
#   BIN_DIR — the folder of the link; default /usr/local/bin
# It always builds again. It makes the link only when no file has the name.

if [ $# -ne 1 ]; then
  echo "Usage: $0 <cli>" >&2
  exit 2
fi
NAME="${1%/}"
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/${NAME}"
BIN_DIR="${BIN_DIR:-/usr/local/bin}"
if [ ! -x "${DIR}/scripts/build.sh" ]; then
  echo "error: ${DIR}/scripts/build.sh does not exist" >&2
  exit 2
fi

OUTPUT="${DIR}/build/${NAME}"
"${DIR}/scripts/build.sh" "$(git -C "${DIR}" rev-parse --short HEAD)" "${OUTPUT}"

LINK="${BIN_DIR}/${NAME}"
if [ "$(readlink "${LINK}" 2>/dev/null)" = "${OUTPUT}" ]; then
  echo "${LINK} already links to the build"
elif [ -e "${LINK}" ] || [ -L "${LINK}" ]; then
  echo "warning: ${LINK} exists and is not a link to ${OUTPUT}; it stays as it is" >&2
else
  ln -s "${OUTPUT}" "${LINK}"
  echo "Linked ${LINK} → ${OUTPUT}"
fi
