# axiom-cli

A small Go CLI that wraps the [Axiom REST API](https://axiom.co/docs/restapi/introduction). All output is JSON; pipe into `jq` for further processing. Its `serve` command is the Trebi adapter that polls the monitor history. The catalog entry `catalog/axiom` uses it. The skill is `catalog/axiom/skill/SKILL.md`.

## Build

```bash
go build -o axiom-cli .
./scripts/build.sh            # writes ./build/axiom-cli with version metadata
```

## Auth

The CLI resolves the token in this order:

1. `--token` flag
2. `AXIOM_TOKEN` env var
3. `~/.cli-tools/axiom-cli/config.json` (written by `auth set`)

Personal access tokens (`xapt-...`) also need an org id, supplied via `--org-id`, `AXIOM_ORG_ID`, or in the config file. API tokens (`xaat-...`) ignore the org header.

```bash
# Save token to ~/.cli-tools/axiom-cli/config.json (mode 0600)
./axiom-cli auth set --token xaat-XXXXXXXXXXXX

# Validate the saved token against /v2/datasets
./axiom-cli auth status

# Print the config file contents (token masked)
./axiom-cli auth show

# Delete the config file
./axiom-cli auth clear
```

## Examples

```bash
# Datasets
./axiom-cli datasets list
./axiom-cli datasets get --id my-dataset
./axiom-cli datasets fields list --dataset my-dataset

# APL query
./axiom-cli query apl 'my-dataset | summarize count() by bin(_time, 1h)' \
    --start 2026-05-05T00:00:00Z --end 2026-05-06T00:00:00Z

# Legacy structured query (body pass-through)
./axiom-cli query legacy --dataset my-dataset --body @query.json

# Ingest from stdin (auto-detected JSON)
echo '{"_time":"2026-05-06T00:00:00Z","level":"info","msg":"hello"}' \
    | ./axiom-cli ingest my-dataset --file -

# Ingest NDJSON file
./axiom-cli ingest my-dataset --file events.ndjson

# Monitors and history
./axiom-cli monitors list
./axiom-cli monitors history --id mon-123 \
    --startTime 2026-05-01T00:00:00Z --endTime 2026-05-06T00:00:00Z

# Annotations
./axiom-cli annotations list --datasets my-dataset --start 2026-05-01T00:00:00Z

# Compact one-line JSON
./axiom-cli datasets list --compact
```

## Body pass-through (`--body`)

Create/update commands accept a `--body` flag in three forms:

- inline JSON: `--body '{"name":"foo"}'`
- file: `--body @./payload.json`
- stdin: `--body -`

The body is validated as JSON before sending. The CLI does not model the request schema — refer to the [Axiom endpoint reference](https://axiom.co/docs/restapi/introduction) for each resource's required fields.

## Subcommands

| Subcommand | Resource |
|---|---|
| `auth` | Token management (set, status, show, clear) |
| `datasets` | Datasets, fields, map-fields, tags, metrics, trim, vacuum |
| `query` | APL, legacy, batch queries |
| `ingest` | Stream JSON / NDJSON / CSV into a dataset |
| `monitors` | Monitors + history |
| `notifiers` | Notifiers |
| `annotations` | Annotations (deploys, releases, incidents) |
| `orgs` | Organizations |
| `users` | Users + current user (`whoami` via `users current`) |
| `tokens` | API tokens |
| `dashboards` | Dashboards |
| `views` | Views |
| `virtual-fields` | Virtual fields |
| `starred` | Saved (starred) queries |
| `groups` | Groups |
| `roles` | Roles |

Run `./axiom-cli <command> --help` for full subcommand listings.

## Pagination

The CLI does not auto-paginate. Where supported, pass `--cursor` and re-run the command with the next cursor value extracted from the response.

## Use with Trebi

Trebi runs `axiom-cli serve` with `TREBI_STATE_DIR` set. The values come only from the inputs of the connection: `AXIOM_TOKEN`, `AXIOM_ORG_ID`, and `AXIOM_EVERY` (the poll interval, default and minimum `1m`). The config file in the home folder does not count.

- With no token, `serve` sends `status` `auth_required` with reason `missing_input`. A token that Axiom refuses gives reason `revoked`.
- `--token`, `--org-id`, and `--api-url` fail with "Trebi sets this value". `auth set` and `auth clear` fail with "Set this value in Trebi".
- `serve` lists the monitors (`GET /v2/monitors`) and reads the history of each (`GET /v2/monitors/{id}/history`). A history entry with state `open` gives `state.changed`, and one with state `closed` gives `state.recovered`. The first poll of a monitor only records the time.
- The state folder holds `axiom-monitors.json` (the cursor of each monitor, format 1). A newer format gives `status` `error` and changes nothing. Outside Trebi, `serve` keeps it in `~/.cli-tools/axiom-cli/`.
- `serve --sandbox` polls a fake API (`internal/fakeaxiom`) in the same process with a 1 s interval. The monitor "Too many errors" triggers and resolves in turn.

The rules for all connector programs are in "Adapter folder contract" in `../../CLAUDE.md`.
