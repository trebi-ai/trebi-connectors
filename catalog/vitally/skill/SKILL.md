---
name: vitally-cli
description: >
  Go CLI for the Vitally (vitally.io) customer-success REST API (paths under
  /resources/*). Use to list, inspect, create, update, and delete accounts,
  users, organizations, conversations, notes, tasks, projects, meetings, NPS
  responses, and custom objects + their instances; look up admins and custom-
  survey responses/questions; link tasks to projects and accounts/orgs to
  instances; and run concurrent client-side bulk delete/create/update (Vitally
  has no native bulk endpoint). Authenticates with a single API key via HTTP
  Basic auth (key as username, empty password). US (subdomain) and EU data
  centers. Triggers: "vitally", "vitally accounts", "vitally users", "vitally
  health score", "create vitally note", "vitally task", "vitally nps", "vitally
  custom object", "bulk delete vitally", "vitally bulk", "list vitally accounts",
  "vitally project", "vitally meeting", "vitally survey responses", "vitally admin".
---

# vitally-cli — Vitally REST API CLI

Go CLI (`urfave/cli/v3`) wrapping the Vitally customer-success REST API. Tables by
default; `-o json` / `-j` for JSON. Binary: `vitally-cli`.
Build: `scripts/build.sh` → `build/vitally-cli`.
API reference: <https://docs.vitally.io/en/collections/10410457-rest-api>.

## Auth

Single API key, sent with **HTTP Basic auth**: the key is the username and the
password is empty (`Authorization: Basic base64(<key>:)`). Get the key in Vitally:
Settings → Operations → Integrations → toggle on "Vitally REST API".

Endpoint depends on the data center (resolved by `config.BaseURL()`):
- US (default): `https://<subdomain>.rest.vitally.io` — needs `--subdomain`.
- EU: `https://rest.vitally-eu.io` — set `--data-center eu`.
- `--base-url` overrides both.

- `vitally-cli auth login` — interactive (data center, subdomain, key); or pass
  `--api-key`/`-k`, `--subdomain`, `--data-center`, `--base-url`. Saved to
  `~/.cli-tools/vitally-cli/config.toml` (mode 0600).
- `vitally-cli auth status` — verify the key reaches the API.
- `vitally-cli auth show` — print endpoint + redacted key.
- `vitally-cli auth logout` — delete the config file.

Resolution order: flag → env (`VITALLY_API_KEY`, `VITALLY_SUBDOMAIN`,
`VITALLY_DATA_CENTER`, `VITALLY_BASE_URL`) → config. `VITALLY_CONFIG` overrides the
config path; `VITALLY_DEBUG=1` == `-V`.

## Conventions

- Most `:id` args accept the Vitally id **or** your `externalId`. Notes and tasks
  need `--source <integration>` to address by `externalId`.
- Updates are **PUT**. Traits: repeatable `--trait key=value` (numbers/bools
  inferred), `--trait-null key` or empty value removes a trait, `--traits-json '{…}'`
  for full control.
- Pagination is cursor-based: `--limit` (max 100), `--from <cursor>`, `--sort-by
  createdAt|updatedAt`, `--all` to follow every page (bounded by `--limit` when >0).
- `delete` on accounts/organizations is permanent; on tasks it archives.
- **User deletes are scheduled, not immediate.** Vitally answers `DELETE
  /resources/users/:id` with `204 No Content` as soon as it accepts the request,
  but the user stays readable — `user get <id>` still returns it and its
  `updatedAt` moves to the time of the call. The Vitally UI then shows a banner:
  "This user was marked to be deleted on <date>." Removal happens on Vitally's
  side at that date. So a `204`, a `✓` from this CLI, or an `ok` count from
  `bulk delete` all mean "marked", never "gone".
- **The REST API does not expose the delete marker.** A marked user returns the
  same payload as any other — no `deletedAt`, no `deleteOn`, no status field. The
  banner is UI-only. To verify a batch from the API, compare `updatedAt` against
  the time you ran the deletes; it is a proxy, since a trait sync moves it too.

## Nouns

### account (alias: accounts)
- `list [--status active|churned|activeOrChurned] [--organization-id ID] [--all]` — cols: ID, External ID, Name, MRR, Updated.
- `get <id>` — full account (KV).
- `create --external-id X --name N [--organization-id ID --trait k=v]`.
- `update <id> [--name --organization-id|--organization-id-null --trait]`.
- `delete <id>` — permanent.
- `health-scores <id>` — health-score breakdown.
- `users <id>` — users on the account.
- `list-related {notes|tasks|conversations|projects|meetings|nps} <account-id>`.
- `bulk {delete|create|update}` — see Bulk below.

### user (alias: users)
- `list [--account-id ID --organization-id ID --all]`.
- `get <id>`; `search (--external-id|--email|--email-subdomain)`.
- `create --external-id X (--account-ids ID… | --organization-ids ID…) [--name --email --avatar --join-date --trait]`.
- `update <id> [...]`; `delete <id> [--delete-on TS]`; `bulk {delete|create|update}`.

### organization (aliases: org, orgs)
- `list [--all]`, `get <id>`, `create --external-id X --name N [--trait]`, `update <id>`, `delete <id>`, `bulk *`.

### conversation (alias: conv)
- `list [--account-id --organization-id --all]`, `get <id>`,
  `create --subject S [--external-id --messages-json '[…]' --trait]`, `update <id>`, `delete <id>`, `bulk *`.

### note (alias: notes)
- `list [--archived --source --account-id --organization-id --all]`, `get <id> [--source]`,
  `create (--account-id|--organization-id) --note HTML --note-date TS [--subject --author-id --category-id --tag --external-id --trait]`,
  `update <id> [--source --account-id --organization-id ...]`, `delete <id>`, `categories`, `bulk *`.
- Reassign a note to another account: `note update <id> --account-id <dest>` (`accountId` is editable on PUT).

### task (alias: tasks)
- `list [--archived --source --account-id --organization-id --all]`, `get <id> [--source]`,
  `create --name N (--account-id|--organization-id) [--description --due-date --completed-at --assigned-to-id --category-id --tag --trait]`,
  `update <id> [--source --account-id --organization-id ...]`, `delete <id>` (archives), `categories`, `bulk *`.
- Reassign a task to another account: `task update <id> --account-id <dest>` (`accountId` is editable on PUT).

### project (alias: projects)
- `list [--archived --account-id --organization-id --all]`, `get <id>`,
  `create --template-id T (--account-id|--organization-id) [--name --target-start-date --target-completion-date --project-status-id --project-category-id --owned-by-vitally-user-id --trait]`,
  `update <id>`, `delete <id>`,
  `move <current-account-id> <project-id> (--to-account-id|--to-organization-id) [--reparent-tasks]` —
  projects cannot be reassigned via PUT; they use the dedicated move endpoint. `--reparent-tasks`
  (default true) carries tasks linked to the project; standalone account tasks are not affected.
  `link-task <project-id> <task-id> [--milestone --source]`, `unlink-task <project-id> <task-id> [--source]`,
  `templates [--category-id]`, `categories`, `bulk *`.

### meeting (alias: meetings)
- `list [--archived --account-id --organization-id --all]`, `get <id>`,
  `create --title T --external-id X --participants-json '[…]' [--description --location --start --end --recording-url --summary --source --trait]`,
  `update <id>`, `delete <id>`,
  `participant add <meeting-id> --json '{…}'`, `participant remove <meeting-id> <participant-id>`,
  `transcript get <meeting-id>`, `transcript set <meeting-id> --json-file F`, `transcripts` (list), `bulk *`.
- A meeting has no `accountId`. Its account link is derived from the participants, but only
  the calendar sync does that derivation. A meeting made with `create` stays at `accounts: []`
  even when the participants resolve to the same users, and Vitally does not support meeting
  updates over REST. Meetings are therefore export-only: read and archive them, do not migrate.

### nps
- `list [--target accounts|organizations --account-id --organization-id --all]`, `get <id>`,
  `create --user-id U --score 0-10 --responded-at TS [--feedback --external-id]`, `update <id>`, `delete <id>`, `bulk *`.

### custom-object (aliases: custom-objects, co)
- `list`, `get <id>`, `create --name N --label L --write-mode W [--custom-fields-json]`, `update <id>`.
- `instances list <custom-object-id> [--archived --all]`.
- `instances bulk delete <custom-object-id> [instance-id…]` — **the headline bulk-delete target**.
- `instance search <custom-object-id> (--id|--external-id|--customer-id|--organization-id|--custom-field-id+--custom-field-value)`.
- `instance create <custom-object-id> --name N [--external-id --customer-id --organization-id --description --owned-by-vitally-user-id --trait]`.
- `instance update <custom-object-id> <instance-id> [...]`; `instance delete <custom-object-id> <instance-id>`.
- `instance link {account|organization|unlink-account|unlink-organization} <custom-object-id> <instance-id> <key> [--source]`.

### survey (alias: surveys) — read-only
- `responses <survey-id>`, `response <response-id>`, `question <question-id>`.

### admin (alias: admins) — read-only
- `search --email E` — cols: ID, Name, Email, License.

## Bulk (concurrent, client-side)

Vitally has no native bulk endpoint, so `bulk` runs individual requests through a
worker pool. Available as `<noun> bulk {delete|create|update}` and
`custom-object instances bulk delete <custom-object-id>`.

- Inputs (auto-detected): positional ids; `--ids a,b,c`; `--ids-file` (one id per
  line, or a CSV with an `id` column); `--json-file` (`-` for stdin) — a JSON array
  of ids (delete) or objects (create/update; update objects need an `id`).
- `--concurrency`/`-c N` (default 5), `--dry-run` (no calls), `--continue-on-error`
  (default), `--stop-on-error`. Prints an `{ok, failed, errors[]}` summary; per-item
  progress to stderr. Exits non-zero if any item failed.

```bash
vitally-cli account bulk delete id1 id2 id3 --concurrency 10
echo '["a","b"]' | vitally-cli account bulk delete --json-file - --dry-run
vitally-cli custom-object instances bulk delete <co-id> --ids-file ids.csv
vitally-cli account bulk update --json-file updates.json   # each object needs "id"
```

## Notes for agents

- Prefer `-j` and pipe to `jq` for structured reads.
- For destructive bulk runs, do a `--dry-run` first, then drop it.
- Rate limit is 1000 req/min (writes count as several); keep `--concurrency` modest
  on large runs.
