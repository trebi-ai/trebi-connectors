---
name: plusvibe-cli
description: >
  Go CLI for PlusVibe.ai — a cold-email outreach platform. Use when the user
  wants to manage workspaces, campaigns, leads, sending email accounts, or
  the unified inbox; pull campaign analytics; bulk-import leads/accounts from
  CSV/JSON; or send/reply/forward messages from the unibox. Authenticates
  with a PlusVibe API key. Triggers: "list plusvibe campaigns", "add lead to
  campaign", "pause campaign", "plusvibe analytics", "send email from
  unibox", "bulk import leads", "warmup status", "plusvibe workspace".
---

# plusvibe-cli — Skill Reference

**What it is.** `plusvibe-cli` is a Go CLI wrapper around the PlusVibe.ai
REST API (`https://api.plusvibe.ai/api/v1/*`). It covers workspaces,
campaigns, leads, sending accounts, analytics, and the unified inbox.

**Installed?** Run `plusvibe-cli auth status`. Expected: `OK — base URL
https://api.plusvibe.ai reachable, api key valid`.

## 1. Global flags (place BEFORE the subcommand)

| Flag | Meaning |
| --- | --- |
| `-k, --api-key <key>` | Override the saved API key for this invocation. |
| `-w, --workspace <id>` | Override the default workspace. |
| `--base-url <url>` | One-off override of the saved base URL. |
| `-o, --output table\|json` | `table` (default) for humans; `json` for scripting. **Use `-o json` when piping to `jq`.** |
| `-j, --json` | Shorthand for `--output json`. |
| `-V, --verbose` | Print HTTP request/response details to stderr (also `PLUSVIBE_DEBUG=1`). |

Env-var overrides for config: `PLUSVIBE_API_KEY`, `PLUSVIBE_WORKSPACE`,
`PLUSVIBE_BASE_URL`, `PLUSVIBE_CONFIG` (override config file path).

Credential resolution order is **flag → env var → config file**, same as the
other cli-tools.

## 2. Command tree

```
plusvibe-cli auth          login | status | logout | show
plusvibe-cli config        show | path
plusvibe-cli workspace     list | add | use <id>
plusvibe-cli campaign      list | get <id> | create | update <id> |
                           delete <id> | activate <id> | pause <id> |
                           subsequence create <campaign-id>
plusvibe-cli lead          list | get <email> | add <campaign-id> |
                           delete | update <email> | status <email> | count
plusvibe-cli email-account list | status <email> | vitals <domain> |
                           delete <email> | warmup (enable|pause|stats) |
                           bulk (add|update) | tags
plusvibe-cli analytics     summary | stats | all
plusvibe-cli unibox        list | others | unread-count | reply | forward |
                           compose | mark-read <thread-id> | draft |
                           delete-thread <thread-id> | delete-message <id>
plusvibe-cli validate      spintax [text] | email [body]
```

Aliases: `workspace`→`ws`, `email-account`→`ea`.

## 3. Auth

```bash
plusvibe-cli auth login                                # interactive
plusvibe-cli auth login --api-key XXXX --workspace W   # non-interactive
plusvibe-cli auth status                               # verify
plusvibe-cli auth show                                 # print masked config
plusvibe-cli auth logout                               # wipe config file
```

Config file: `~/.cli-tools/plusvibe-cli/config.toml`, mode 0600. Fields:
`base_url`, `api_key`, `default_workspace`.

## 4. Workspaces

```bash
plusvibe-cli workspace list                # human table
plusvibe-cli -j workspace list             # JSON for scripts
plusvibe-cli workspace add --name "New"    # create
plusvibe-cli workspace use <workspace-id>  # save as default
```

Most subsequent commands require a workspace; resolve via `-w` flag,
`PLUSVIBE_WORKSPACE`, or `workspace use`.

## 5. Campaigns

```bash
plusvibe-cli campaign list                              # default table
plusvibe-cli campaign list --status ACTIVE
plusvibe-cli campaign get <campaign-id>
plusvibe-cli campaign create --name "Q2 outbound"
plusvibe-cli campaign activate <campaign-id>
plusvibe-cli campaign pause <campaign-id>
plusvibe-cli campaign update <campaign-id> --name "Renamed"
plusvibe-cli campaign delete <campaign-id>              # archives by default
plusvibe-cli campaign delete <campaign-id> --archive=false  # permanent

plusvibe-cli campaign subsequence create <campaign-id> --name "Step 2"
```

## 6. Leads

```bash
# List + filter + page
plusvibe-cli lead list
plusvibe-cli lead list --campaign <id> --status active --page 2 --page-size 100

plusvibe-cli lead get user@example.com

# Single add
plusvibe-cli lead add <campaign-id> \
  --email user@example.com --first-name Jane --last-name Doe --company Acme \
  --var role=CTO --var industry=SaaS

# Bulk import (CSV, JSON array, or NDJSON)
plusvibe-cli lead add <campaign-id> --file leads.csv
plusvibe-cli lead add <campaign-id> --file leads.json

# Update + status changes
plusvibe-cli lead update user@example.com --company Acme2 --var tier=enterprise
plusvibe-cli lead status user@example.com --campaign <id> --status paused

# Delete one or more
plusvibe-cli lead delete --emails user1@example.com --emails user2@example.com

# Aggregate counts per status
plusvibe-cli lead count --campaign <id>
```

CSV/JSON recognized keys: `email` (required), `first_name`/`firstName`,
`last_name`/`lastName`, `company`/`company_name`, plus any other column or
field becomes a custom variable. JSON files may be a single array OR
newline-delimited JSON (NDJSON).

## 7. Sending email accounts

```bash
plusvibe-cli email-account list
plusvibe-cli ea list --tags "warm,senior"
plusvibe-cli ea status sender@acme.com
plusvibe-cli ea vitals acme.com              # SPF/DKIM/DMARC report
plusvibe-cli ea delete sender@acme.com

# Warmup
plusvibe-cli ea warmup enable sender@acme.com
plusvibe-cli ea warmup pause sender@acme.com
plusvibe-cli ea warmup stats sender@acme.com --start-date 2026-04-01 --end-date 2026-05-01

# Bulk SMTP
plusvibe-cli ea bulk add --file smtp_accounts.csv
plusvibe-cli ea bulk update --file smtp_updates.csv

# Tags
plusvibe-cli ea tags --emails a@x.com --emails b@x.com --action add --tags warm --tags vip
plusvibe-cli ea tags --emails a@x.com --action remove --tags cold
```

## 8. Analytics

```bash
plusvibe-cli analytics summary --campaign-id <id>
plusvibe-cli analytics stats --campaign-id <id> --start-date 2026-04-01 --end-date 2026-05-01
plusvibe-cli analytics all                          # all campaigns aggregate
plusvibe-cli -j analytics stats --campaign-id <id>  # JSON for scripting
```

## 9. Unified inbox (unibox)

```bash
plusvibe-cli unibox list
plusvibe-cli unibox list --campaign-id <id> --lead user@example.com --preview
plusvibe-cli unibox others                          # non-campaign mail
plusvibe-cli unibox unread-count

plusvibe-cli unibox compose \
  --to recipient@example.com \
  --subject "Following up" \
  --body "Hi there — checking in." \
  --campaign-id <id>

plusvibe-cli unibox reply \
  --reply-to-id <email-id> \
  --from sender@acme.com --to lead@example.com \
  --subject "Re: ..." --body "Thanks!"

plusvibe-cli unibox forward --email-id <id> --to colleague@acme.com --body "FYI"

plusvibe-cli unibox draft --to lead@example.com --subject "WIP" --body "..."

plusvibe-cli unibox mark-read <thread-id>
plusvibe-cli unibox delete-thread <thread-id>
plusvibe-cli unibox delete-message <message-id>
```

## 10. Validate (offline, no API key required)

`validate` runs entirely locally — no API call, no workspace, no credentials.

```bash
plusvibe-cli validate spintax "{{random|Hi|Hello}}"     # inline text
plusvibe-cli validate spintax --file body.md            # from a file
plusvibe-cli validate spintax -t "{{first_name}}"       # via --text flag
plusvibe-cli validate email --subject "Hi {{first_name}}" --file body.md
plusvibe-cli -j validate spintax "{{random}}"           # JSON output
```

- Input comes from `--file`, `--text`/`-t`, or a positional argument — supply
  exactly one.
- Errors print as a list (all of them, not just the first); warnings print in
  a separate list.
- Process exits non-zero **iff there are errors**. Warnings never fail.
- `-o json` returns `{valid, errors[], warnings[]}`; each issue carries
  `severity`, `message`, `line`, `column`, `snippet`.

Spintax dialect is PlusVibe double-brace: `{{random|a|b}}`,
`{{fallback|{{first_name}}|Sir}}`, `{{merge_tag}}`. Only one merge tag is
allowed inside any single `random`/`fallback` section; an empty option
(`{{random||Hello}}`) is intentionally valid.

## 11. Output conventions

- **`table` (default)**: aligned columns for lists; labeled key/value blocks
  for single-record commands.
- **`-o json` (or `-j`)**: prints the API response indented. **Use this for
  any output consumed by another tool.** Lists are typically
  `{ "leads": [...], "totalCount": N }` or a bare JSON array — inspect the
  shape with `--verbose` if unsure.

## 12. Common workflows for an agent

**Find active campaigns and their reply rates.**
```bash
plusvibe-cli -j campaign list --status ACTIVE \
  | jq -r '.[].id' \
  | while read id; do
      plusvibe-cli -j analytics stats --campaign-id "$id" \
        | jq -r '[.campaign_name, .total_sent, .reply_rate] | @tsv'
    done
```

**Bulk-import leads from a CSV.**
```bash
plusvibe-cli lead add <campaign-id> --file ./leads.csv
plusvibe-cli lead count --campaign <campaign-id>   # confirm
```

**Triage unread mail in the unibox.**
```bash
plusvibe-cli -j unibox list --preview \
  | jq -r '.[] | select(.is_read == false) | "\(._id)\t\(.from)\t\(.subject)"'
```

## 13. Important behaviors and gotchas

- **Workspace is required for most calls.** Always run `workspace use <id>`
  or pass `-w` so commands don't error with "no workspace configured".
- **Auth = API key only.** No OAuth. The key goes in the `X-API-Key` header.
- **`campaign delete` archives by default.** Pass `--archive=false` for
  permanent deletion.
- **Bulk add/update flag parsing**: `--emails` and `--tags` are repeatable
  (use `--emails a@x.com --emails b@x.com`). `--var` is the same for custom
  lead variables.
- **CSV header names are flexible.** Both `first_name` and `firstName` work.
- **All addresses are validated server-side** (not locally), unlike
  inbox-cli. Bad input surfaces as an HTTP 400 with the API error body.
- **`-v` is `--version`**, not verbose. Use `--verbose` or `-V`
  (or `PLUSVIBE_DEBUG=1`) for HTTP tracing.

## 14. Where things live

- Build script: `plusvibe-cli/scripts/build.sh` → `plusvibe-cli/build/plusvibe-cli`
- Config file: `~/.cli-tools/plusvibe-cli/config.toml` (mode 0600)
- Client types: `internal/client/*.go`
- API base: `https://api.plusvibe.ai`

When in doubt, `plusvibe-cli <command> --help` prints the exact flags.
