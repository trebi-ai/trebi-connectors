#!/usr/bin/env bash
# Prints the files that changed between a base commit and HEAD, one per line.
# CI uses it to test only the parts that changed.
#
#   scripts/changed.sh <base-sha>
#
# Prints "*" when there is no usable base: a manual run, a new branch, or a
# base that the remote does not have after a force push. The caller then
# tests everything.
set -euo pipefail

base="${1:-}"
if [ -z "$base" ] || [ -z "${base//0/}" ] || ! git fetch -q --depth=1 origin "$base" 2>/dev/null; then
  echo '*'
  exit 0
fi
git diff --name-only "$base" HEAD
