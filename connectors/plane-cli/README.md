# plane-cli

Command-line client for the [Plane](https://plane.so) REST API (Cloud or self-hosted).

It covers the full API surface. A resource table (`internal/spec/spec.json`) makes the
subcommands from the [API reference](https://developers.plane.so/api-reference/introduction).
The CLI uses the v2 API where it exists, and the v1 API for v1-only resources (epics,
relations, work item pages, search, teamspace members and projects, archived cycles and modules).

## Install

```
scripts/build.sh [version] [output]   # builds build/plane-cli and build/plane-op
```

Or install it from source. `go install` does not create `plane-op`; copy the program to that name for the operations:

```
go install github.com/trebi-ai/trebi-connectors/connectors/plane-cli@latest
```

## Auth

Make a personal access token in Plane: Profile Settings → Personal Access Tokens.

```
plane-cli auth login                         # interactive: key, host, workspace, project
plane-cli auth login --api-key plane_api_... --workspace acme --project ENG
plane-cli auth status                        # GET /api/v2/users/me/
```

The config is at `~/.cli-tools/plane-cli/config.toml` (mode 0600). The CLI resolves
each value in this order: flag → env var → config file.

| Flag | Env | Config key |
|---|---|---|
| `--api-key/-k` | `PLANE_API_KEY` | `api_key` |
| `--host` | `PLANE_HOST` | `host` (default `https://api.plane.so`) |
| `--workspace/-w` | `PLANE_WORKSPACE` | `workspace` |
| `--project/-p` | `PLANE_PROJECT` | `project` |

`-W/--workspace-scope` uses the workspace variant of a command that also has a project variant.

A token works only in the workspace that made it. Save a key for each other workspace:

```
plane-cli auth workspace add acme   # prompts for the key, tests it, saves it
plane-cli -w acme project list       # uses that key
plane-cli auth workspace list
```

The config keeps it under `[workspaces.<slug>]` (`api_key`, optional `host`). The order is
flag → env var → workspace entry → top-level value.

`PLANE_CONFIG` changes the config file path. `plane-cli config set <key> <value>` changes one value.

## Usage

```
plane-cli <noun> [<sub-noun>] <verb> [ids] [flags]
```

```
plane-cli project list
plane-cli -p ENG wi list --all -c sequence_id,name,priority
plane-cli -p ENG wi create --name "Login fails" --priority high --state Todo --labels bug
plane-cli wi get ENG-42 -j
plane-cli wi update ENG-42 --state Done
plane-cli wi comment create ENG-42 --comment-md '**Fixed** in `main`'
plane-cli -p ENG wi create --name Spec --description-md @spec.md
plane-cli wi attachment upload ENG-42 ./trace.log
plane-cli wi attachment download ENG-42 -d ./files   # all attachments of the work item
plane-cli asset download <id> -d ./files             # workspace asset
plane-cli page list -W                       # workspace pages, also when a project is set
plane-cli api GET 'workspaces/{workspace}/members/'
plane-cli listen -p ENG                      # poll: work item changes as JSON lines
plane-cli listen --tunnel                    # webhook through a cloudflared quick tunnel
```

- A work item key (`ENG-42`) also sets the project.
- Each API param has a typed flag. `--data`, `--set k=v`, and `--null k` set any body field.
  `--filter/-q k=v` sets any query param.
- `--description-md` and `--comment-md` take GitHub Flavored Markdown (or `@file`, `-`) and send it as HTML.
  Bad input (empty, not UTF-8, an unclosed code fence) gives an error, and the CLI sends nothing.
- `wi update` refuses a closed state (group `completed` or `cancelled`) on a work item with open
  sub-items at any level. `--force` skips this check. `--dry-run` prints the result of the check on stderr.
- `--all` follows the pages. `--dry-run` prints the request and does not send it.
- `-o json` / `-j` prints JSON. `-c` selects table columns (dotted paths are allowed).
- `-V` prints HTTP details to stderr. `PLANE_DUMP_BODY=1` also prints the bodies.

- `listen` prints change events as JSON lines. Plane has no streaming API. `--poll` (default) lists changed
  work items each `--interval` and needs only the API key. `--tunnel` creates a temporary webhook to a
  `cloudflared` quick tunnel: real time, all event types, deletes included. It needs a workspace admin.
- `serve` is the [Trebi](https://github.com/trebi-ai/trebi) connector (trebi-connector/1). It polls the
  workspaces and projects of `PLANE_PROJECTS` (for example `acme/*, beta/ENG`) and sends work item,
  state, comment, intake, and customer changes as events. `serve --sandbox` runs against a fake Plane.
  `op search|get|list` (also `plane-op <name>`) are its read-only operations. The catalog entry is
  `catalog/plane`.

Run `plane-cli <command> --help` to see the flags, the API path, and the docs link.
See `catalog/plane/skill/SKILL.md` for the full command list.

## Use with Trebi

Trebi starts `plane-cli serve` with `TREBI_STATE_DIR` set. In this mode:

- The settings come only from the connection inputs: `PLANE_API_KEY`, `PLANE_PROJECTS`, `PLANE_HOST`, and `PLANE_POLL_INTERVAL`. The CLI does not read `~/.cli-tools/plane-cli/config.toml` or `PLANE_CONFIG`.
- `--api-key` and `--host` fail with "Trebi sets this value".
- `auth login`, `auth logout`, `auth workspace add`, `auth workspace remove`, and `config set` fail with "Set this value in Trebi".
- The default workspace is the first workspace of `PLANE_PROJECTS`.
- Only `serve` writes the state: `plane-state.json` in `TREBI_STATE_DIR`.
- A key works only in the workspace that made it. Add one connection for each workspace.

## Regenerate the spec

```
python3 -I scripts/gen-spec.py                 # downloads the API reference pages
python3 -I scripts/gen-spec.py --pages <dir>   # uses a local copy of the pages
```

## Tests

```
go test ./...
```

The tests use `httptest` servers. They do not send requests to Plane.
