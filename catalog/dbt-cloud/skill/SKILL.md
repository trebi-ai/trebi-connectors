---
name: dbt-cloud-cli
description: >
  Go CLI for the dbt Cloud Administrative API (v2). Use to list and inspect dbt
  Cloud accounts, projects, environments, and jobs; trigger job runs (with
  optional git-branch/sha/schema/step overrides and --wait polling); list and
  inspect runs and their steps; cancel runs; and list/download run artifacts
  (manifest.json, run_results.json, catalog.json). Authenticates with a single
  service token or personal API key. Supports US/EMEA/AU/single-tenant hosts.
  Triggers: "dbt cloud", "trigger dbt job", "dbt cloud run", "dbt run status",
  "cancel dbt run", "list dbt jobs", "dbt cloud artifacts", "download
  manifest.json from dbt", "dbt cloud projects", "dbt cloud environments",
  "dbt cloud api".
---

# dbt-cloud-cli — dbt Cloud Administrative API CLI

Go CLI (`urfave/cli/v3`) wrapping the dbt Cloud Administrative API v2
(`https://cloud.getdbt.com/api/v2/accounts/<id>/...`). Tables by default; `-o
json` / `-j` for JSON. Binary: `dbt-cloud-cli`.
Build: `scripts/build.sh` → `build/dbt-cloud-cli`.

## Auth

A single token — either a **service token** (Account settings → Service tokens,
best for automation) or a **personal API key** (Profile → API access). Sent as
`Authorization: Token <token>`. **No OAuth.**

One-time setup:

```bash
dbt-cloud-cli auth login                 # prompts for the token (hidden)
dbt-cloud-cli auth login -t <token> -a <account-id> --host <host>
dbt-cloud-cli account list               # find your account ID
dbt-cloud-cli account use <id>           # set the default account
```

Most commands need an **account ID**. Resolution order (token, account, host):
flag → env → config file. Per-command override: `--account-id/-a`.

**Region/host**: default is `https://cloud.getdbt.com` (US multi-tenant). For
EMEA (`https://emea.dbt.com`), AU (`https://au.dbt.com`), newer cell-based hosts
(`https://<prefix>.us1.dbt.com`), or single-tenant, set `--host` (global or on
`auth login`) or `DBT_CLOUD_CLI_HOST`.

- `dbt-cloud-cli auth status` — verify the token and list accessible accounts.
- `dbt-cloud-cli auth show` — print config, token redacted.
- `dbt-cloud-cli auth logout` — delete the config.

Env overrides: `DBT_CLOUD_CLI_TOKEN`, `DBT_CLOUD_CLI_ACCOUNT_ID`,
`DBT_CLOUD_CLI_HOST`, `DBT_CLOUD_CLI_CONFIG`, `DBT_CLOUD_CLI_DEBUG=1`.

## Accounts

```bash
dbt-cloud-cli account list               # accounts the token can reach
dbt-cloud-cli account get [id]           # one account
dbt-cloud-cli account use <id>           # set default in config
```

## Projects

```bash
dbt-cloud-cli project list               # projects in the default account
dbt-cloud-cli project list -a 12345      # in a specific account
dbt-cloud-cli project get <project-id>
```

## Environments

```bash
dbt-cloud-cli environment list
dbt-cloud-cli environment list -p <project-id>   # filter by project
```

## Jobs

```bash
dbt-cloud-cli job list                    # all jobs in the account
dbt-cloud-cli job list -p <project-id>    # filter by project
dbt-cloud-cli job get <job-id>            # definition + execute steps

# Trigger a run. --cause is required by the API (defaulted here).
dbt-cloud-cli job run <job-id>
dbt-cloud-cli job run <job-id> -c "manual backfill"
dbt-cloud-cli job run <job-id> --git-branch feature/x --schema-override dbt_pr
dbt-cloud-cli job run <job-id> --step "dbt seed" --step "dbt run -s model+"
dbt-cloud-cli job run <job-id> --wait            # poll until complete; exit 1 on error
dbt-cloud-cli job run <job-id> --wait --poll-interval 5
```

`job run` returns the created run (with its ID and `href`). With `--wait` it
polls and exits non-zero if the run errors.

## Runs

```bash
dbt-cloud-cli run list                     # recent runs, newest first (default 20)
dbt-cloud-cli run list -j <job-id>         # for one job
dbt-cloud-cli run list --status running    # queued|starting|running|success|error|cancelled
dbt-cloud-cli run list -n 50 --offset 50   # paginate
dbt-cloud-cli run get <run-id>             # run + its steps
dbt-cloud-cli run cancel <run-id>          # cancel an in-progress run
```

Run status codes: `1` queued, `2` starting, `3` running, `10` success,
`20` error, `30` cancelled (shown by name in tables).

## Artifacts

```bash
dbt-cloud-cli run artifacts list <run-id>                       # available paths
dbt-cloud-cli run artifacts get <run-id> manifest.json          # to stdout
dbt-cloud-cli run artifacts get <run-id> run_results.json -o ./run_results.json
dbt-cloud-cli run artifacts get <run-id> catalog.json -o ./catalog.json
```

The artifact endpoint returns the file body directly (not the JSON envelope);
`get` writes raw bytes to stdout or to `--out/-o`.

## Notes for agents

- Account ID is required for everything except `account list` and `auth`. If a
  command errors with "no account ID configured", run `account list` then
  `account use <id>` (or pass `-a`).
- `--json`/`-j` returns the unwrapped `data` payload from the API envelope.
- Only the v2 Administrative API is implemented (the stable, widely-used routes:
  accounts, projects, environments, jobs, runs, artifacts). The Discovery
  (metadata) and Semantic Layer GraphQL APIs are separate and not covered.
```
