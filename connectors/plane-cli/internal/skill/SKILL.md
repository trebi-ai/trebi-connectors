---
name: plane-cli
description: >
  Go CLI for the Plane (plane.so) project-management REST API, Cloud or
  self-hosted. Covers the full API surface: projects, work items (issues) and
  their comments, links, attachments, worklogs, activity, relations, and
  property values; states, labels, cycles, modules, milestones, epics,
  initiatives, intake, pages, views, templates, work item types and custom
  properties, workflows, estimates, customers, releases, teamspaces, members,
  invitations, webhooks, automations, stickies, and assets. Uses the v2 API
  where it exists and the v1 API for v1-only resources. Authenticates with a
  personal access token in the X-Api-Key header. Triggers: "plane", "plane.so",
  "plane work item", "plane issue", "create plane issue", "plane project",
  "plane cycle", "plane module", "plane epic", "plane page", "plane comment",
  "plane state", "plane label", "plane-cli". `plane-cli serve` is the Trebi
  connector (trebi-connector/1); `plane-op search|get|list` runs its read-only
  operations.
---

# plane-cli — Plane REST API CLI

Go CLI (`urfave/cli/v3`) for the Plane REST API. Output is a table by default.
Use `-o json` or `-j` for JSON. Binary: `plane-cli`.
Source: `connectors/plane-cli` in `github.com/trebi-ai/trebi-connectors`.
Build: `scripts/build.sh [version] [output]` → `build/plane-cli` and `build/plane-op`.
API reference: <https://developers.plane.so/api-reference/introduction>.

## Auth and defaults

<!-- managed:omit -->

- Token: Plane → Profile Settings → Personal Access Tokens. It goes in the `X-Api-Key` header.
- `plane-cli auth login` — interactive. Or pass `--api-key`, `--host`, `--workspace`, `--project`.
  Saved to `~/.cli-tools/plane-cli/config.toml` (mode 0600).
- `plane-cli auth logout`.
- `plane-cli config show|path|set <key> [value]` — keys: `workspace`, `project`, `host`, `api_key`.
- A token works only in the workspace that made it. For a second workspace, save its key:
  `plane-cli auth workspace add <slug>` (hidden prompt; or `--api-key K`, `--api-key -` for stdin, `--host`).
  The CLI tests the key and saves nothing if it fails. Then `-w <slug>` uses that key.
  `auth workspace list|remove <slug>`.
- A 401 "credentials were not valid" with `-w <slug>` means that the key is not for that workspace
  (a wrong slug gives 404).
- Order of precedence: flag → env → `[workspaces.<slug>]` entry → top-level config value.

<!-- /managed:omit -->

- `plane-cli auth status` — calls `GET /api/v2/users/me/`.
- `plane-cli auth show` — prints the settings in use. The key is masked.

| Global flag | Env | Meaning |
|---|---|---|
| `--workspace/-w` | `PLANE_WORKSPACE` | Workspace slug (from the URL) |
| `--project/-p` | `PLANE_PROJECT` | Project UUID or identifier (for example `ENG`) |
| `--workspace-scope/-W` | | Use the workspace variant of a command that has a workspace and a project variant |
| `--output/-o`, `--json/-j` | | `table` or `json` |
| `--columns/-c` | | Table columns, comma-separated. Dotted paths are allowed (`state.name`). |
| `--verbose/-V` | `PLANE_DEBUG` | Print HTTP details to stderr. `PLANE_DUMP_BODY=1` also prints bodies. |

<!-- managed:omit -->

| Login flag | Env | Meaning |
|---|---|---|
| `--api-key/-k` | `PLANE_API_KEY` | Token |
| `--host` | `PLANE_HOST` | Default `https://api.plane.so`. Set it for self-hosted. |

`PLANE_CONFIG` changes the config file path.

In a Trebi connection (`TREBI_STATE_DIR` is set), Trebi sets the key (`PLANE_API_KEY`) and the address (`PLANE_HOST`). The CLI reads no config file: `~/.cli-tools/plane-cli` and `PLANE_CONFIG` are not read. `--api-key` and `--host` fail with "Trebi sets this value". `auth login`, `auth logout`, `auth workspace add`, `auth workspace remove`, and `config set` fail with "Set this value in Trebi". The default workspace is the first workspace of `PLANE_PROJECTS`; use `-w <slug>` for another one.

<!-- /managed:omit -->

Managed mode (OverSire): when `PLANE_MANAGED_BY=OverSire` is set, OverSire sets `PLANE_API_KEY`, `PLANE_HOST`, `PLANE_WORKSPACE`, and `PLANE_PROJECT`. The CLI reads no config file. `-w` and `-p` still work in the CLI. The host app can refuse them.

<!-- managed:omit -->

In managed mode, `~/.cli-tools/plane-cli` and `PLANE_CONFIG` are not read. `--api-key` and `--host` fail with "OverSire sets this value". `auth login`, `auth logout`, `auth workspace add`, `auth workspace remove`, and `config set` fail with "Set this value in OverSire". `listen` and `serve` fail with "OverSire does not run listen" and "OverSire does not run serve".

<!-- /managed:omit -->

## Identifiers

- A project is a UUID or its identifier (`ENG`).
- A work item is a UUID or its key (`ENG-123`). A key also sets the project, so `-p` is not necessary:
  `plane-cli wi comment list ENG-123`.
- v1 endpoints need UUIDs. The CLI finds the UUIDs for you (project identifier and `ENG-123` keys).
- Work item id lists in a body (`--add`, `--remove`, `--issues`, `--work-items`, `--work-item-ids`) on
  work item endpoints also accept `ENG-123` keys. The CLI changes them to UUIDs.
- Work item create and update accept names: `--state "In Progress"`, `--labels bug,auth`,
  `--assignees ana@example.com`, `--type Bug`, `--parent ENG-118`, `--estimate 5`.
- `wi update` refuses a closed state (group `completed` or `cancelled`) on a work item with open sub-items
  at any level. It exits with code 1 and sends nothing:
  `ENG-1 has 2 open sub-items: ENG-2, ENG-5. Close them first, or add --force`.
  Close the sub-items first. Use `--force` only when the user tells you to keep them open.
  `--dry-run` prints the result of this check on stderr.

## Markdown

Plane stores rich text as HTML. It does not read Markdown. Each HTML body field has a Markdown flag:
`--description-md` (for `--description-html`) and `--comment-md` (for `--comment-html`).
The value is Markdown text, `@file`, or `-` for stdin.

- The CLI converts GitHub Flavored Markdown with goldmark: headings, emphasis, `~~strike~~`, autolinks,
  tables, fenced code with a language, block quotes, lists.
- A bullet list in which each item is a task (`- [ ]` / `- [x]`) becomes a Plane checklist
  (`data-type="taskList"`). A list with other items, or a numbered list, keeps a plain checkbox.
- Raw HTML in the Markdown goes through as it is.
- The CLI rejects bad input with an error and sends no request: empty input, input that is not UTF-8,
  NUL bytes, and a code fence that is not closed (`markdown: line 12: code fence "```" is not closed`).
- Do not use the `-md` flag and the `-html` flag together.
- Prefer `-md` flags for descriptions and comments. Use `@file` or `-` for long text, to keep shell quoting simple.

## Command shape

`plane-cli <noun> [<sub-noun>] <verb> [positional ids] [flags]`

- Positional args are the path ids other than workspace and project, in path order.
  Example: `wi comment get ENG-12 <comment-id>`.
- Each body and query param has a typed flag in kebab-case (`--target-date`, `--label-ids`).
  Array flags are repeatable or comma-separated. Run `<command> --help` to see the flags,
  the API path, and the docs link.
- Generic flags on each API command:
  - `--data/-d '<json>'` (or `@file`, `-` for stdin) — the base body. Typed flags go on top of it.
  - `--set key=value` — any body field. The CLI infers the value type (number, bool, JSON).
  - `--null key` — send `null`.
  - `--filter/-q key=value` — any query param (for example `-q state_group__in=started,completed`).
  - `--all/-a` and `--max N` — on GET lists, follow all pages (cursor first, offset as fallback).
  - `--dry-run` — print the request. It does not send it.
  - `--workspace-scope/-W` (global, before or after the command) — on commands with a workspace and a project
    variant (marked `*` below). With a project set, the project variant is the default. `-W` forces the workspace variant.
- List tables show a page footer on stderr. Use `--all` for all items, or `--filter per_page=200`.
- Upsert commands print `upsert: created|updated` on stderr.
- Bulk commands take `--data '{"items":[...]}'` or `{"ids":[...]}` (max 50 items). Add `"all_or_none": true` as necessary.
  The table shows `index`, `result`, `id`, `code`, `detail` for each item, then `succeeded: N  failed: N`.
  A failed item does not give a non-zero exit code. Read `failed`.

## Commands

`*` = workspace and project scope. `¹` = v1 API.

```
project, proj: list get create update delete archive unarchive summary upsert bulk-create bulk-update create-from-template¹
  project features: get update
work-item, wi, issue: list* get* create update delete archive unarchive upsert search¹ advanced-search¹
                      bulk-create bulk-update bulk-delete
  comment: list get create update delete upsert bulk-create bulk-update bulk-delete
  link: list get create update delete
  attachment: list get create confirm delete upload download
  worklog: list get create update delete summary
  activity: list get
  relation: list¹ create¹ remove¹
  page: list¹ get¹ add¹ delete¹
  property-value: list¹ get¹ set¹ update¹ delete¹
work-item-type, type: list* get* create* update* delete* enable import schema mark-default*
  property: list* get* attach* detach*
property, prop: list* get* create* update* delete*
  option: list* get* create* update* delete*
  context: list get create update delete
state | label | estimate | estimate point: list get create update delete upsert bulk-create bulk-update bulk-delete
cycle: list get create update delete transfer upsert manage-work-items list-work-items¹
       archive¹ unarchive¹ list-archived¹ bulk-create bulk-update bulk-delete
module: like cycle, without transfer
milestone: list get create update delete upsert manage-work-items list-work-items¹ bulk-*
epic: list¹ get¹ create¹ update¹ delete¹ list-work-items¹ add-work-items¹
initiative: list get create update delete manage-labels manage-projects manage-work-items
            list-labels¹ list-projects¹ list-epics¹ add-epics¹ remove-epics¹
  label: list get create update delete
intake: list get create update delete
page: list* get* create* update* delete* archive¹* unarchive¹*
collection: list get create update delete members manage-members manage-pages search-pages list-pages¹ move-page¹
view | template: list* get* create* update* delete*   (template also: use)
workflow, workflow state, workflow transition: list get create update delete
customer: list get create update delete upsert manage-work-items list-work-items¹
  property: list get create update delete
  property-value: list set get¹ update¹
  request: list get create update delete
release: list get create update delete changelog update-changelog manage-labels manage-work-items list-labels¹ list-work-items¹
  comment | label | link | tag: list get create update delete
teamspace: list get create update delete
  member | project: list¹ add¹ remove¹
member: list* get create update delete remove role-distribution
invitation: list get create bulk-create update¹ delete
webhook: list get create update delete regenerate-secret
  log: list get
automation: list* get* create* update* delete* status*
  node: list* get* create* update* delete* regenerate-webhook-secret*
  edge: list* get* create* update* delete*
  activity: list* get*
group-sync config: get update
group-sync workspace-mapping | project-mapping: list get create update delete
project-label: list¹ get¹ create¹ update¹ delete¹
sticky: list get create update delete
op: search get list   (JSON on stdin; plane-op is the same)
skill: --raw   (print this reference as Markdown; needs no API key)
asset: list get create confirm delete upload
user: me
  asset: list get create confirm delete upload
artifact: create get update publish
audit-log: list get
role | permission-scheme: list get
permission: me*
workspace features: get update
```

<!-- managed:omit -->

Commands that run until you stop them:

```
listen: --poll (default) | --tunnel | --public-url   (JSON lines, see "Listen to changes")
serve: --projects --interval --sandbox   (Trebi connector, see "Trebi connector")
```

<!-- /managed:omit -->

## Uploads

`upload` runs three steps: create the record, POST the file to the storage URL, then confirm.

```
plane-cli wi attachment upload ENG-12 ./screenshot.png
plane-cli asset upload ./logo.png
plane-cli user asset upload ./avatar.png --entity-type USER_AVATAR
```

## Downloads

`wi attachment download` and `asset download` save files under their own names. Without ids, `wi attachment download` saves all uploaded attachments of the work item. `asset download` needs ids, because the asset list holds every attachment of the workspace.
The v2 API gives only metadata, so the commands use the v1 detail endpoint. It gives a presigned storage URL: a redirect for an attachment, JSON `asset_url` for an asset. The commands do not send the API key to storage.

```
plane-cli wi attachment download ENG-12                  # all attachments, into .
plane-cli wi attachment download ENG-12 <id> -d ./files  # one attachment, into ./files
plane-cli wi attachment download ENG-12 <id> -O out.png  # set the file path
plane-cli wi attachment download ENG-12 <id> -O - | jq . # write to stdout
plane-cli asset download <id> [<id>...] -d ./files       # workspace assets (ids from `asset list`)
```

Flags: `-d/--dir` (default `.`), `-O/--out` (one file only; `-` is stdout), `-f/--force` (overwrite). With `-j`, it prints `[{id,name,size,path}]`.

<!-- managed:omit -->

## Listen to changes

`plane-cli listen` prints one JSON line per event on stdout until Ctrl-C. Status lines go to stderr.
Plane has no streaming API, so there are two modes:

- `--poll` (default): each `--interval` (default 15s), list work items with `updated_at__gte` the last change.
  Needs only the API key. Sees work item create and update only: not deletes, archives, comments
  (a comment does not change `updated_at`), links, or other resources.
  `previous_attributes` has the old values of fields that changed after the listen started.
  `--activities` adds the activity rows (`field`, `old_value`, `new_value`) of each change.
  `--since 1h` (or RFC 3339) also prints older changes. `-q key=value` adds list filters.
- `--tunnel`: start a local server and a `cloudflared` quick tunnel (no account), then create a temporary
  workspace webhook. Real time, and all webhook events: work items, comments, links, attachments,
  relations, votes, projects, cycles, modules, milestones, pages. Deletes carry the old record in
  `previous_attributes`. Needs a workspace admin and `cloudflared` on PATH. The CLI checks
  `X-Plane-Signature`, drops repeated `event_id`s, and deletes the webhook on exit. It also deletes
  webhooks named `plane-cli listen <host> <pid>` of stopped runs on this host.
  `--public-url URL --port N` uses your own tunnel (for example ngrok) in place of cloudflared.

Common flags: `-p ENG` keeps only the events of that project. `-W` listens to the whole workspace.
`--events 'workitem.*,cycle.created'` selects event names (globs). In tunnel mode it also sets the webhook scopes.

Each line has the shape of a Plane v2 webhook payload, plus `source` (`poll`/`webhook`) and `key` (`ENG-42`):
`{"source","event","entity_type","entity_id","key","event_id","data","previous_attributes","activities"}`.

```
plane-cli listen -p ENG --activities | jq -c '{event,key,changes:.previous_attributes}'
plane-cli listen --tunnel --events 'workitem.*'
```

## Trebi connector

`plane-cli serve` speaks trebi-connector/1 on stdin and stdout. Trebi starts it; do not run it by hand.
It polls Plane each `PLANE_POLL_INTERVAL` (default 60s) and sends one event for each change.

- Scope: `PLANE_PROJECTS` (or `--projects`), for example `acme/*, beta/ENG`. `*` or a bare slug is
  all projects of the workspace. In Trebi mode each workspace uses `PLANE_API_KEY`. Outside Trebi, each
  workspace uses its `[workspaces.<slug>]` key, or `PLANE_API_KEY`. A key works only in the workspace
  that made it, so a Trebi connection watches one workspace.
- Room: `<workspace>/<project identifier>`, for example `acme/ENG`. A customer event has the
  workspace slug as its room. Sender: the member who made the change (`bot: true` for Plane system users).
- Event types: `work_item.created`, `work_item.updated`, `work_item.state_changed`, `comment.created`,
  `intake.created`, `intake.accepted`, `intake.declined`, `intake.duplicate`, `intake.snoozed`, `customer.updated`.
- `data` has `workspace`, `project`, `work_item_id`, `identifier` (`ENG-42`), `name`, `url`, `state`,
  `state_group`, `labels`, `priority`, and the change: `field`, `old`, `new`. `work_item.updated` also has
  `changes` (all changed fields). `work_item.state_changed` also has `old_state_group`. Intake events have
  `intake_id` and `status` (`pending`, `accepted`, `declined`, `snoozed`, `duplicate`). A move back to pending
  sends no event. Comment events have `comment` and `comment_id`.
- A work item that waits in intake sends only intake events, until intake accepts it.
- The first start saves the current state and sends nothing. The state (`plane-state.json` in
  `TREBI_STATE_DIR`) keeps the cursors, so a restart sends no old event again.
- Limits: comments do not change `updated_at`, so each poll reads the comments of at most 10 work items
  that changed in the last 3 days. Deletes and archives send no event. Customers are polled only for a
  workspace with `*`.
- `--sandbox` runs against a fake Plane in the process (`internal/fakeplane`), with one scripted change each second.

Operations (read-only, JSON on stdin, JSON on stdout). The manifest runs them as `plane-op <name>`:

```
echo '{"query":"login bug","project":"ENG"}' | plane-cli op search
echo '{"key":"ENG-42"}' | plane-cli op get
echo '{"project":"ENG","state_group":"started","label":"agent","limit":20}' | plane-cli op list
```

Each input also takes `workspace`. When `PLANE_PROJECTS` is set, a project outside it is an error.
`scripts/build.sh` also writes `plane-op` (a copy of the program): `plane-op get` is `plane-cli op get`. Trebi does not let an operation program also be a run command.

The catalog entry is `catalog/plane`. Check it from the repository root with `scripts/validate.sh --conformance plane`.

<!-- /managed:omit -->

## Raw requests

`plane-cli api <METHOD> <path>` sends any request. A path without `/api/` gets `/api/v2/`.
The CLI fills `{workspace}`/`{slug}` and `{project}`/`{project_id}` from the config.

```
plane-cli api GET 'workspaces/{workspace}/projects/'
plane-cli api GET /api/v1/workspaces/{workspace}/projects/ --all
plane-cli api PATCH 'workspaces/{workspace}/work-items/ENG-12/' -d '{"priority":"high"}'
```

## Examples

```
plane-cli project list -c id,identifier,name
plane-cli -p ENG wi list --all -c sequence_id,name,state.name,priority -q expand=state
plane-cli -p ENG wi create --name "Login fails" --priority high --state Todo --labels bug
plane-cli wi update ENG-42 --state Done
plane-cli wi comment create ENG-42 --comment-md 'Fixed in #123. **Deployed** to staging.'
plane-cli -p ENG wi create --name "Spec" --description-md @spec.md
git log -5 --oneline | sed 's/^/- /' | plane-cli wi comment create ENG-42 --comment-md -
plane-cli wi relation create ENG-42 --relation-type blocking --issues <uuid>
plane-cli -p ENG cycle manage-work-items <cycle-id> --add ENG-42,ENG-43
plane-cli page list -W
plane-cli -p ENG view create --name "Urgent" --filters '{"priority":["urgent"]}' --display-filters '{"layout":"list","group_by":"state"}'
plane-cli view create --name "All urgent" --filters '{"priority":["urgent"]}'   # workspace view (no project set, or -W)
plane-cli -p ENG intake create --name "Bug report" --source-email user@example.com
plane-cli -p ENG intake update <intake-id> --status 1                            # accept
```

## Notes for agents

- Run `--help` on a command before you use it. The help shows the exact flags and required fields.
- Use `--dry-run` to check a write before you send it.
- Use `-j` when you parse output. The JSON is the raw API response (list envelopes included),
  except for `--all`, which gives a JSON array.
- Errors from v2 use RFC 9457 (`code`, `detail`, field `errors`). The CLI prints them.
  On 429, the CLI waits for `Retry-After` and tries again.
- `--filters` (a view body field) is not `--filter/-q` (a query param). JSON body fields that the docs
  call "string" (`filters`, `display_filters`, `display_properties`, `logo_props`, `view_props`, `config`,
  `settings`, `metadata`) go as JSON when the value starts with `{` or `[`, or is `@file` or `-`.
  Bad JSON gives an error, and the CLI sends nothing.
- View columns are `display_properties` (a map of column → bool). Child items are `display_filters.sub_issue`.
  A PATCH replaces the whole object, so send all keys:
  `plane-cli -W view get <id> -j -q fields=display_properties` → change → `view update <id> --display-properties @file`.
- Intake: the project needs `intake_view` on (`project update <p> --set intake_view=true`).
  Intake `status`: -2 pending, -1 declined, 0 snoozed, 1 accepted, 2 duplicate. Create also makes a
  linked work item (`work_item_id`).
- Some features must be on in the project first (`project features update --is-epic-enabled`,
  worklogs, intake). Epics also need a Plane plan that has them. If not, the API gives 404 "Epic is not enabled".
- There is no API to list workspaces. Get the slug from the Plane URL.
- v2 lists: `per_page` default 50, max 200. Offset max 10000. Use `--all` for large sets.
