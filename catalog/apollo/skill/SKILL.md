---
name: apollo-cli
description: >
  Apollo.io REST API CLI (https://docs.apollo.io/reference). Use when the user wants to search
  or enrich people / organizations, manage CRM accounts / contacts / deals, run or pause email
  sequences, log calls or tasks, list emailer messages, or pull API usage stats. Authenticates
  with a single Apollo API key. Triggers: "apollo people search", "enrich contact apollo",
  "apollo sequences", "list apollo accounts", "apollo enrich domain", "apollo opportunities",
  "apollo bulk match", "apollo usage stats".
---

# apollo-cli — Apollo.io REST API CLI

Go CLI wrapping the Apollo.io v1 REST API. JSON output, pretty-printed.
Binary: `apollo-cli`.
Build: `scripts/build.sh` → `build/apollo-cli`.

## Auth

Apollo uses a single API key sent in the `X-Api-Key` header (not `Authorization: Bearer`,
not HTTP Basic). Keys come from the Apollo dashboard under Settings → Integrations → API.

Resolution order: `--api-key` flag → `APOLLO_API_KEY` env → `~/.cli-tools/apollo-cli/config.json` (mode 0600).

```bash
apollo-cli auth set --api-key XXXXXXXXXXXXXXXX [--api-url URL]
apollo-cli auth status              # validates against GET /labels
apollo-cli auth show                # config with api_key masked
apollo-cli auth clear               # removes config file
```

`auth status` semantics:
- `validated: ok` — key is a master key.
- `validated: ok (key valid; non-master, /labels forbidden)` — key works but isn't master-tier.
- `validated: invalid (401)` — key is wrong.

## Global flags

| Flag | Env | Purpose |
| --- | --- | --- |
| `--api-key` | `APOLLO_API_KEY` | Apollo API key (sent as `X-Api-Key`) |
| `--api-url` | `APOLLO_API_URL` | base URL (default `https://api.apollo.io/api/v1`) |
| `--compact` | — | single-line JSON output |

## Command tree

```
apollo-cli auth            set | status | show | clear
apollo-cli accounts        search | create | update --id | view --id |
                           update-stage | update-owners | bulk-create | bulk-update | list-stages
apollo-cli contacts        search | create | update --id | view --id |
                           update-stages | update-owners | bulk-create | bulk-update |
                           list-stages | associated-deals --id
apollo-cli deals           search | view --id | create | update --id | list-stages
apollo-cli people          search | enrich | bulk-enrich
apollo-cli organizations   search | enrich --domain | bulk-enrich | view --id |
                           job-postings --id | news
apollo-cli sequences       search | add-contacts --id | remove-contacts |
                           activate --id | deactivate --id | archive --id
apollo-cli emails          search | stats --id
apollo-cli calls           search | create | update --id
apollo-cli tasks           search | create | bulk-create
apollo-cli users           list
apollo-cli email-accounts  list
apollo-cli labels          list                    # master-key only (403 otherwise)
apollo-cli fields          list | create | list-custom
apollo-cli notes           list
apollo-cli usage           stats
apollo-cli reports         sync
```

Every command's `--help` lists its subcommands; explore with `apollo-cli <group> --help`.

## How requests work — `--body` pass-through

Apollo endpoints accept a **single JSON object** body (not an array — unlike DataForSEO).
Per-endpoint param schemas are too numerous to model as flags, so the CLI takes the body
verbatim via `--body`:

```bash
--body '{"q_keywords":"founder","page":1}'   # inline
--body @./query.json                         # file
--body -                                     # stdin
```

The body is JSON-validated locally before sending. Schemas:
`https://docs.apollo.io/reference/<endpoint-slug>`.

### When `--body` is optional

For POSTs that semantically take no body, `--body` is optional and the CLI sends `{}`:

- `sequences activate` / `deactivate` / `archive`
- `contacts associated-deals`
- `usage stats`

All other POSTs and every PATCH/PUT require `--body`.

### Special non-body endpoint

`organizations enrich` is a `GET /organizations/enrich?domain=...` — use `--domain`
(no `--body`):

```bash
apollo-cli organizations enrich --domain apollo.io
```

## Examples by group

### People & organizations (Apollo-wide directory)

```bash
apollo-cli people search --body '{"q_keywords":"engineering manager","page":1}'
apollo-cli people enrich --body '{"email":"tim@apple.com"}'
apollo-cli people bulk-enrich --body '{"details":[{"email":"a@x.com"},{"email":"b@y.com"}]}'

apollo-cli organizations search --body '{"q_organization_name":"Apollo"}'
apollo-cli organizations enrich --domain apollo.io
apollo-cli organizations bulk-enrich --body '{"domains":["a.com","b.com"]}'
apollo-cli organizations view --id 65f...
apollo-cli organizations job-postings --id 65f...
apollo-cli organizations news --body '{"organization_ids":["65f..."]}'
```

### CRM (accounts, contacts, deals)

```bash
apollo-cli accounts search --body '{"page":1}'
apollo-cli accounts create --body '{"name":"Acme","domain":"acme.com"}'
apollo-cli accounts update --id 65f... --body '{"phone":"+15551234"}'
apollo-cli accounts view --id 65f...
apollo-cli accounts list-stages

apollo-cli contacts create --body '{"first_name":"Jane","last_name":"Doe","email":"j@x.com"}'
apollo-cli contacts associated-deals --id 65f...

apollo-cli deals search
apollo-cli deals create --body '{"name":"Q2 ACV","amount":"50000"}'
apollo-cli deals list-stages
```

### Sequences (emailer campaigns)

```bash
apollo-cli sequences search --body '{"page":1}'
apollo-cli sequences add-contacts --id 65f... \
    --body '{"contact_ids":["..."],"send_email_from_email_account_id":"..."}'
apollo-cli sequences remove-contacts --body '{"contact_ids":["..."],"emailer_campaign_ids":["..."]}'
apollo-cli sequences activate --id 65f...
apollo-cli sequences deactivate --id 65f...
apollo-cli sequences archive --id 65f...
```

### Activity (emails, calls, tasks)

```bash
apollo-cli emails search
apollo-cli emails stats --id 65f...

apollo-cli calls create --body '{"to_number":"+15551234","contact_id":"65f..."}'
apollo-cli calls update --id 65f... --body '{"note":"Left voicemail"}'

apollo-cli tasks create --body '{"user_id":"...","contact_ids":["..."],"type":"call","due_at":"2026-06-01T00:00:00Z"}'
apollo-cli tasks bulk-create --body '{"tasks":[...]}'
```

### Admin / metadata

```bash
apollo-cli users list
apollo-cli email-accounts list
apollo-cli labels list                      # 403 unless using a master key
apollo-cli fields list
apollo-cli fields create --body '{"name":"My Field","type":"text","field_type":"contact"}'
apollo-cli notes list
apollo-cli usage stats                      # accepts optional --body filter
apollo-cli reports sync --body '{...}'
```

## Gotchas

- **`X-Api-Key`, not Bearer.** Apollo's auth header is `X-Api-Key: <key>`. The Apollo docs
  explicitly reject the key in any other location with `INVALID_API_KEY_LOCATION` (the
  error message even includes the doc URL: https://docs.apollo.io/docs/test-api-key).
- **Body is a single object.** If the docs show an array somewhere, that's a field *inside*
  the body (e.g. `{"contact_ids":[...]}`), not the body itself.
- **Master-key endpoints.** `GET /labels` and some bulk endpoints require a master key —
  a regular key returns 403. The error is from Apollo, not the CLI.
- **IDs are 24-char hex strings** (MongoDB ObjectIds), e.g. `65f1abc123...`.
- **Search filters differ across endpoints.** `people search` uses `q_keywords` /
  `person_titles[]` / `organization_locations[]`; `accounts search` uses different keys.
  Always check the per-endpoint reference page.
- **`deals search` is a GET**, not a POST — filters are query params on the URL. The CLI
  doesn't currently expose those as flags; pipe the resulting JSON through `jq` or use the
  Apollo dashboard for complex filtering.

## Paths

- Binary: `/usr/local/bin/apollo-cli` (symlinked by `register.sh`).
- Config: `~/.cli-tools/apollo-cli/config.json` (mode 0600).
- This skill is hard-linked into `~/.claude/skills/apollo-cli/SKILL.md` and
  `~/.agents/skills/apollo-cli/SKILL.md`.
