#!/usr/bin/env bash
# Publish the catalog to the R2 bucket behind https://catalog.trebi.ai/v1/.
# Needs R2_ACCOUNT_ID, R2_ACCESS_KEY_ID, R2_SECRET_ACCESS_KEY, and
# CATALOG_SIGNING_KEY. A snapshot key that exists is never written again:
# the same bytes are skipped, and other bytes fail the run. Extra arguments
# go to "catalogctl build".
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
bucket="${R2_BUCKET:-trebi-catalog}"
: "${R2_ACCOUNT_ID:?}" "${R2_ACCESS_KEY_ID:?}" "${R2_SECRET_ACCESS_KEY:?}" "${CATALOG_SIGNING_KEY:?}"
export AWS_ACCESS_KEY_ID="$R2_ACCESS_KEY_ID" AWS_SECRET_ACCESS_KEY="$R2_SECRET_ACCESS_KEY" AWS_DEFAULT_REGION=auto
export GOWORK=off
s3() { aws --endpoint-url "https://${R2_ACCOUNT_ID}.r2.cloudflarestorage.com" "$@"; }
exists() { s3 s3api head-object --bucket "$bucket" --key "$1" >/dev/null 2>&1; }

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# Read the previous index from the bucket, not through the cache.
: >"$work/previous.json"
if exists v1/index.json; then
  s3 s3 cp --only-show-errors "s3://$bucket/v1/index.json" "$work/previous.json"
fi

(cd "$root/tools/catalogctl" && go run . build --root "$root" --previous "$work/previous.json" --out "$work/out" "$@")

cd "$work/out"
if [ -d v1/snapshots ]; then
  find v1/snapshots -type f -name '*.tar.gz' | sort | while read -r key; do
    if exists "$key"; then
      s3 s3 cp --only-show-errors "s3://$bucket/$key" "$work/published.tar.gz"
      if cmp -s "$key" "$work/published.tar.gz"; then
        echo "skip $key (published with the same bytes)"
        continue
      fi
      echo "FAIL $key is published with other bytes; bump the version" >&2
      exit 1
    fi
    s3 s3api put-object --bucket "$bucket" --key "$key" --body "$key" \
      --content-type application/gzip --cache-control "public, max-age=31536000, immutable" >/dev/null
    echo "put  $key"
  done
fi
if [ -d v1/icons ]; then
  for f in v1/icons/*.svg; do
    s3 s3api put-object --bucket "$bucket" --key "$f" --body "$f" \
      --content-type image/svg+xml --cache-control "public, max-age=3600" >/dev/null
    echo "put  $f"
  done
fi
for f in v1/index.json v1/index.json.sig; do
  type=application/json
  [ "$f" = v1/index.json.sig ] && type=text/plain
  s3 s3api put-object --bucket "$bucket" --key "$f" --body "$f" \
    --content-type "$type" --cache-control "public, max-age=300" >/dev/null
  echo "put  $f"
done
