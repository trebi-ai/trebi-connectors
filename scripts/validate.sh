#!/usr/bin/env bash
# Validate the catalog. With no names, it checks every entry.
#
#   scripts/validate.sh [--conformance] [--check-assets] [name...]
#
# --conformance runs "trebi connector conformance --manifest catalog/<name>"
# for each entry. When connectors/<bin> has the program of the entry, it
# builds the program first and puts it on PATH. It needs a trebi on PATH
# that has the conformance command.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
conformance=0
flags=()
names=()
for a in "$@"; do
  case "$a" in
    --conformance) conformance=1 ;;
    --check-assets) flags+=(--check-assets) ;;
    *) names+=("$a") ;;
  esac
done

(cd "$root/tools/catalogctl" && GOWORK=off go run . validate --root "$root" ${flags[@]+"${flags[@]}"} ${names[@]+"${names[@]}"})

# The schema is vendored from trebi. Compare it when a sibling checkout exists.
upstream="$root/../trebi/internal/connectors/manifest/schema.json"
if [ -f "$upstream" ] && ! cmp -s "$upstream" "$root/schema/trebi-connector.schema.json"; then
  echo "FAIL schema/trebi-connector.schema.json differs from $upstream" >&2
  exit 1
fi

[ "$conformance" = 1 ] || exit 0
if ! command -v trebi >/dev/null || ! trebi connector conformance --help >/dev/null 2>&1; then
  echo "FAIL trebi on PATH has no \"connector conformance\" command; install a newer trebi release" >&2
  exit 1
fi

bin="$(mktemp -d)"
trap 'rm -rf "$bin"' EXIT
export PATH="$bin:$PATH"
failed=()
entries="$(cd "$root/tools/catalogctl" && GOWORK=off go run . entries --root "$root")"
while IFS=$'\t' read -r name version cli; do
  [ -n "$name" ] || continue
  if [ ${#names[@]} -gt 0 ] && [[ ! " ${names[*]} " =~ " $name " ]]; then continue; fi
  if [ -n "$cli" ] && [ -d "$root/connectors/$cli" ] && [ ! -x "$bin/$cli" ]; then
    "$root/connectors/$cli/scripts/build.sh" "$version" "$bin/$cli" >/dev/null
  fi
  if trebi connector conformance --manifest "$root/catalog/$name"; then
    echo "ok   $name conformance"
  else
    echo "FAIL $name conformance" >&2
    failed+=("$name")
  fi
done <<<"$entries"
if [ ${#failed[@]} -gt 0 ]; then
  echo "FAIL conformance: ${failed[*]}" >&2
  exit 1
fi
