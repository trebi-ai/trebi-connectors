#!/usr/bin/env bash
# Validate the catalog. With no names, it checks every entry.
#
#   scripts/validate.sh [--conformance] [--check-assets] [name...]
#
# --conformance builds the CLI of each trebi-connector/1 entry and checks
# "<cli> serve --sandbox". It runs "trebi connector conformance" when the
# trebi on PATH has that command, and a handshake check otherwise.
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

export GOWORK=off
(cd "$root/tools/catalogctl" && go run . validate --root "$root" ${flags[@]+"${flags[@]}"} ${names[@]+"${names[@]}"})

# The schema is vendored from trebi. Compare it when a sibling checkout exists.
upstream="$root/../trebi/internal/connectors/manifest/schema.json"
if [ -f "$upstream" ] && ! cmp -s "$upstream" "$root/schema/trebi-connector.schema.json"; then
  echo "FAIL schema/trebi-connector.schema.json differs from $upstream" >&2
  exit 1
fi

# A channel entry carries the skill of its CLI. They must stay the same.
entries="$(cd "$root/tools/catalogctl" && go run . protocol-entries --root "$root")"
while IFS=$'\t' read -r name command; do
  [ -n "$name" ] || continue
  if [ ${#names[@]} -gt 0 ] && [[ ! " ${names[*]} " =~ " $name " ]]; then continue; fi
  cli="${command%% *}"
  src="$root/channels/$cli"
  [ -d "$src" ] || continue
  if ! cmp -s "$src/SKILL.md" "$root/catalog/$name/skill/SKILL.md"; then
    echo "FAIL $name: catalog/$name/skill/SKILL.md differs from channels/$cli/SKILL.md" >&2
    exit 1
  fi
  [ "$conformance" = 1 ] || continue

  bin="$(mktemp -d)"
  pkg=.
  [ -d "$src/cmd/$cli" ] && pkg="./cmd/$cli"
  (cd "$src" && go build -tags sqlite_fts5 -o "$bin/$cli" "$pkg")
  sandbox="$bin/$cli serve --sandbox"
  if command -v trebi >/dev/null && trebi connector conformance --help >/dev/null 2>&1; then
    trebi connector conformance --command "$sandbox"
  else
    echo "note: trebi connector conformance is not available; run the handshake check for $name" >&2
    out="$(printf '%s\n' \
      '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocol":"trebi-connector/1","daemon":{"version":"ci"},"instance":{"key":"ci","name":"ci"}}}' \
      '{"jsonrpc":"2.0","method":"initialized"}' \
      '{"jsonrpc":"2.0","id":2,"method":"shutdown"}' \
      | TREBI_STATE_DIR="$bin" timeout 20 $sandbox)"
    grep -q '"protocol":"trebi-connector/1"' <<<"$out" || { echo "FAIL $name: no initialize result" >&2; echo "$out" >&2; exit 1; }
    grep -q '"id":2,"result"' <<<"$out" || { echo "FAIL $name: no shutdown result" >&2; echo "$out" >&2; exit 1; }
  fi
  echo "ok   $name conformance"
done <<<"$entries"
