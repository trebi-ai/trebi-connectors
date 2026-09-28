---
name: hightouch-cli
description: >
  Go CLI for the Hightouch (hightouch.com) public REST API — a reverse-ETL /
  data-activation platform. Use to list and inspect syncs, models, sources, and
  destinations; view sync run history (status, query size, started/finished);
  and trigger sync runs (optionally full-resync, clear-and-fill, or reset-CDC)
  by sync id or slug. Authenticates with a single Hightouch API key (Bearer).
  Triggers: "hightouch", "trigger a hightouch sync", "list hightouch syncs",
  "hightouch sync runs", "hightouch sync status", "run my hightouch sync",
  "hightouch models", "hightouch sources", "hightouch destinations",
  "reverse etl trigger", "full resync hightouch".
---

# hightouch-cli — Hightouch REST API CLI

Go CLI (`urfave/cli/v3`) wrapping the Hightouch public REST API
(`https://api.hightouch.com/api/v1`). Tables by default; `-o json` / `-j` for
JSON. Binary: `hightouch-cli`.
Build: `scripts/build.sh` → `build/hightouch-cli`.
API reference: <https://hightouch.com/docs/api-reference>.

## Auth

API key only — **Bearer token**, no OAuth. The key must be created by an
**Admin** user of the workspace (Hightouch app → Settings → API keys). The key
is workspace-scoped, so there is **no workspace flag** — the key determines the
workspace.

- `hightouch-cli auth login` — prompts for the key (or pass `--api-key`/`-k`),
  stores it in `~/.cli-tools/hightouch-cli/config.toml` (mode 0600).
- `hightouch-cli auth status` — verify the key reaches the API.
- `hightouch-cli auth show` — print base URL + redacted key.
- `hightouch-cli auth logout` — delete the config file.

Resolution order: flag → env (`HIGHTOUCH_API_KEY`, `HIGHTOUCH_BASE_URL`) →
config file. `HIGHTOUCH_CONFIG` overrides the config path.

**Rate limit:** 200 requests / 10 s per workspace.

## Commands

Common list flags: `--name`, `--slug`, `--limit` (default 100), `--offset`,
`--order-by` (`id|name|slug|createdAt|updatedAt`).

### sync
`get`, `runs`, and `trigger` accept **either a numeric id or a slug** — a
non-numeric argument is auto-resolved (slug → id lookup for get/runs; the global
trigger endpoint for trigger). The raw GET/runs API only accepts numeric ids, so
the CLI resolves slugs first.

- `sync list [--model-id ID] [--slug S]` — columns: ID, Slug, Model, Dest,
  Status, Disabled, Last Run (Last Run shown as ISO + relative age, e.g.
  `2026-05-29T15:30:06.731Z (45m ago)`).
- `sync get <id|slug>` — full sync (KV table; JSON includes raw `configuration`,
  `schedule`, `tags`).
- `sync runs <id|slug> [--limit] [--offset] [--after ISO] [--before ISO]
  [--order-by id|createdAt|startedAt|finishedAt]` — run history (Run ID, Status,
  Query Size, Started, Finished; times annotated with relative age).
- `sync trigger <id|slug>` — trigger a run. Numeric → `POST /syncs/{id}/trigger`;
  non-numeric → `POST /syncs/trigger` (by slug). Returns the new run id. If a run
  is in progress, the new one is queued.
  - `--slug` — force slug handling for an all-digit slug.
  - `--full-resync` — resync all rows, ignoring previously synced rows.
  - `--clear-and-fill` — clear the audience then sync the full query.
  - `--reset-cdc` — sync all rows without executing changes on the destination.

### model
- `model list` — ID, Name, Slug, Source, Query Type, Primary Key.
- `model get <model-id>`.

### source (alias `sources`)
- `source list` — ID, Name, Slug, Type.
- `source get <source-id>`.

### destination (aliases `destinations`, `dest`)
- `destination list` — ID, Name, Slug, Type, Syncs (count).
- `destination get <destination-id>`.

### config
- `config show` — resolved config, key redacted.
- `config path` — config file path.

## Notes

- IDs for syncs/models/sources/destinations are numeric; sync trigger also
  accepts a slug via `--slug`.
- `configuration`, `schedule`, and `tags` are passed through as raw JSON — their
  schema is connector-specific and Hightouch may change it; the CLI does not
  interpret them. Use `-j` to see them.
- The API also exposes create/update (`POST`/`PATCH`) for syncs/models/sources/
  destinations, plus decision-engine flows, event contracts, IDR runs, and sync
  sequences. These are **not yet surfaced** by this CLI — add new methods in
  `internal/client/` and a `cmd/<noun>.go` if needed.
- Global flags: `-o json`/`-j`, `-V`/`--verbose` (set `HIGHTOUCH_DUMP_BODY=1`
  with `-V` to dump response bodies).
