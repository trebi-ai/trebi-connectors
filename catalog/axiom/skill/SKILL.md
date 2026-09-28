---
name: axiom-cli
description: >
  Axiom (axiom.co) REST API CLI. Use when the user wants to query Axiom logs/traces with APL,
  inspect datasets, fields, monitors, monitor history, notifiers, annotations, dashboards, views,
  saved queries, virtual fields, orgs, users, tokens, or ingest events.
  Triggers: "query axiom", "axiom apl", "list axiom datasets", "axiom monitors", "axiom monitor history",
  "axiom annotations", "axiom dashboards", "ingest into axiom", "axiom whoami", "axiom tokens".
---

# axiom-cli — Axiom REST API CLI

Go CLI wrapping every read endpoint of the Axiom REST API. JSON output, pretty-printed.
Binary: `axiom-cli`. Build: `scripts/build.sh` → `build/axiom-cli`.

## Auth

Token resolution order: `--token` flag → `AXIOM_TOKEN` env → `~/.cli-tools/axiom-cli/config.json`.
Org id (required for personal tokens `xapt-...`): `--org-id` → `AXIOM_ORG_ID` → config file.

```bash
axiom-cli auth set --token xaat-XXXX [--org-id ID] [--api-url URL]   # writes ~/.cli-tools/axiom-cli/config.json (0600)
axiom-cli auth status     # shows source (flag/env/file), masked token, hits /v2/datasets to validate
axiom-cli auth show       # prints config file with token masked
axiom-cli auth clear      # removes config file
```

## Query (APL is the primary tool)

```bash
# APL — positional query string
axiom-cli query apl 'sling-production | summarize count() by bin(_time, 1h)' \
    --start 2026-05-01T00:00:00Z --end 2026-05-06T00:00:00Z

axiom-cli query apl "['sling-production'] | where data contains '<exec-id>' | order by _time desc | limit 100"
axiom-cli query apl '<APL>' --format tabular   # default; or --format legacy
axiom-cli query apl '<APL>' --no-cache
axiom-cli query apl '<APL>' --cursor <c> --include-cursor

# Legacy structured query — body pass-through
axiom-cli query legacy --dataset my-dataset --body @query.json

# Batch query
axiom-cli query batch --body @batch.json
```

Datasets with dots/dashes in their name must be quoted in APL: `['sling-production-frontend']`.
Time inputs: RFC3339 or relative (`now-1h`, `now-7d`).

Map fields: when a field is typed as `map`, access nested keys with bracket syntax — `where ['data']['lvl'] == "error"` (Axiom's own canonical form) or `where data['lvl'] == 'error'`. Cast with `tostring(...)` if the comparison needs an explicit string. `data.lvl == 'error'` does **not** work. Use `axiom-cli datasets fields list --dataset <name>` to see which fields are maps. `data contains '<substring>'` works for substring search across the whole map without casting.

## Datasets, fields, tags, metrics

```bash
axiom-cli datasets list
axiom-cli datasets get --id my-dataset
axiom-cli datasets create --body '{"name":"foo","description":"..."}'
axiom-cli datasets update --id my-dataset --body @patch.json
axiom-cli datasets delete --id my-dataset
axiom-cli datasets trim   --name my-dataset --max-duration 24h
axiom-cli datasets vacuum --name my-dataset

axiom-cli datasets fields list   --dataset my-dataset
axiom-cli datasets fields get    --dataset my-dataset --field _time
axiom-cli datasets fields update --dataset my-dataset --field _time --body @patch.json

axiom-cli datasets map-fields list   --dataset my-dataset
axiom-cli datasets map-fields create --dataset my-dataset --body @mf.json
axiom-cli datasets map-fields update --dataset my-dataset --body @mf.json
axiom-cli datasets map-fields delete --dataset my-dataset --name attrs

axiom-cli datasets tags list                # /v1/datasets/_tags
axiom-cli datasets tags values
axiom-cli datasets tags metrics
axiom-cli datasets tags metric-tags
axiom-cli datasets tags metric-tag-values
```

## Monitors and history

```bash
axiom-cli monitors list
axiom-cli monitors get    --id mon-123
axiom-cli monitors create --body @monitor.json
axiom-cli monitors update --id mon-123 --body @patch.json
axiom-cli monitors delete --id mon-123
axiom-cli monitors history --id mon-123 \
    --startTime 2026-05-01T00:00:00Z --endTime 2026-05-06T00:00:00Z
```

## Annotations, notifiers, dashboards, views, saved queries

```bash
axiom-cli annotations list --datasets my-dataset --start 2026-05-01T00:00:00Z --end 2026-05-06T00:00:00Z
axiom-cli annotations get|create|update|delete --id <id> [--body @a.json]

axiom-cli notifiers     list|get|create|update|delete [--id <id>] [--body @n.json]
axiom-cli dashboards    list|get|create|update|delete [--id <id>] [--body @d.json]
axiom-cli views         list|get|create|update|delete [--id <id>] [--body @v.json]
axiom-cli starred       list|get|create|update|delete [--id <id>] [--body @s.json]   # saved queries
axiom-cli virtual-fields list|get|create|update|delete [--id <id>] [--body @v.json]
```

## Org / users / tokens

```bash
axiom-cli orgs list
axiom-cli orgs get --id <org-id>
axiom-cli orgs update --id <org-id> --body @o.json

axiom-cli users list
axiom-cli users get --id <user-id>
axiom-cli users current                          # whoami — may 500 with personal access tokens; use `axiom-cli auth status` instead
axiom-cli users update-current --body @u.json
axiom-cli users update-role    --id <user-id> --body '{"role":"admin"}'
axiom-cli users remove         --id <user-id>

axiom-cli tokens list
axiom-cli tokens get        --id <token-id>
axiom-cli tokens create     --body @t.json
axiom-cli tokens regenerate --id <token-id> --body @r.json
axiom-cli tokens delete     --id <token-id>

axiom-cli groups list|get|create|update|delete [--id <id>] [--body @g.json]
axiom-cli roles  list|get|create|update|delete [--id <id>] [--body @r.json]
```

## Ingest

```bash
# Auto-detects content type from extension (.ndjson/.jsonl, .csv, else JSON).
axiom-cli ingest my-dataset --file events.ndjson
axiom-cli ingest my-dataset --file events.csv  --csv-fields "time,level,msg" --csv-delimiter ,
echo '{"_time":"2026-05-06T00:00:00Z","level":"info","msg":"hello"}' \
    | axiom-cli ingest my-dataset --file -

# Optional knobs:
axiom-cli ingest my-dataset --file ev.json \
    --content-type application/json \
    --timestamp-field _time \
    --timestamp-format 2006-01-02T15:04:05Z \
    --event-labels '{"service":"api"}'
```

## `--body` pass-through

Create/update commands take `--body` in three forms:
- inline JSON: `--body '{"name":"foo"}'`
- file: `--body @./payload.json`
- stdin: `--body -`

The body is JSON-validated locally before sending. Schemas are not modeled in the CLI — see `https://axiom.co/docs/restapi/endpoints/<endpoint>` for required fields.

## Output

JSON, pretty-printed. Pipe through `jq -c` if you need single-line output.

```bash
axiom-cli datasets list | jq '.[].name'
axiom-cli datasets list | jq -c '.[]' >> datasets.ndjson
```

## Pagination

The CLI does not auto-paginate. Pass `--cursor` (and `--limit` where supported); re-run with the next cursor extracted from the response.
