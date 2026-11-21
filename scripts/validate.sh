#!/usr/bin/env bash
# Validate the catalog. With no names, it checks every entry.
#
#   scripts/validate.sh [--conformance] [--no-external-cli] [--check-assets] [name...]
#
# --conformance runs "trebi connector conformance --manifest catalog/<name>"
# for each entry. When connectors/<bin> has the program of the entry, it
# builds the program first and puts it on PATH. It needs a trebi on PATH
# that has the conformance command. For a program in connectors/, it adds
# --strict, so a warn row also fails. The fakes in scripts/fakes (for example
# psql) go on PATH first, so an entry that wraps a system program needs no
# server.
#
# --no-external-cli skips the actions.cli suite for an entry whose CLI the
# catalog does not build. CI uses it, because the runner does not have those
# CLIs. Run the full check on your machine with the CLI installed.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
conformance=0
external_cli=1
flags=()
names=()
for a in "$@"; do
  case "$a" in
    --conformance) conformance=1 ;;
    --no-external-cli) external_cli=0 ;;
    --check-assets) flags+=(--check-assets) ;;
    *) names+=("$a") ;;
  esac
done

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
(cd "$root/tools/catalogctl" && GOWORK=off go build -o "$tmp/catalogctl" .)
"$tmp/catalogctl" validate --root "$root" ${flags[@]+"${flags[@]}"} ${names[@]+"${names[@]}"}

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
if [[ "$(trebi connector conformance --help 2>&1)" != *--strict* ]]; then
  echo "FAIL trebi on PATH has no --strict; install a newer trebi release" >&2
  exit 1
fi

bin="$tmp/bin"
mkdir "$bin"
cp "$root"/scripts/fakes/* "$bin"/
export PATH="$bin:$PATH"
failed=()
entries="$("$tmp/catalogctl" entries --root "$root" ${names[@]+"${names[@]}"})"
while IFS=$'\t' read -r name version cli; do
  [ -n "$name" ] || continue
  if [ -n "$cli" ] && [ -d "$root/connectors/$cli" ] && [ ! -x "$bin/$cli" ]; then
    "$root/connectors/$cli/scripts/build.sh" "$version" "$bin/$cli" >/dev/null
  fi
  suites=()
  if [ -n "$cli" ] && [ -d "$root/connectors/$cli" ]; then suites+=(--strict); fi
  if [ -z "$cli" ] && [ "$external_cli" = 0 ]; then
    echo "note $name: the catalog builds no CLI, so the actions.cli suite is skipped"
    for s in manifest install inputs actions.mcp events.lines protocol folder; do suites+=(--suite "$s"); done
  fi
  if trebi connector conformance --manifest "$root/catalog/$name" ${suites[@]+"${suites[@]}"}; then
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
