# slack-cli

Command-line client for the Slack Web API.

`slack-cli` covers the full Web API surface. The command tree comes from the
official method reference, so every method is a subcommand with typed flags.
It reuses the Slack desktop app session, so you do not create an app to start.
Add more workspaces and switch between them. `slack-cli listen` streams live
events as JSON lines. `slack-cli serve` connects the workspaces to Trebi.

## Install

```bash
bash scripts/build.sh        # builds ./build/slack-cli
```

Or build the whole monorepo and register the binaries:

```bash
../build.sh                  # from the repo root
```

## Quick start

```bash
# Import every workspace from the Slack desktop app.
slack-cli auth import

# List workspaces; the default has a "*".
slack-cli auth list

# Post a message (names resolve to ids).
slack-cli chat post-message --channel '#general' --text 'hello from the CLI'

# Read recent messages with names resolved.
slack-cli conversations read '#general' --limit 20

# Stream live events.
slack-cli listen --channel '#general'
```

## Authentication

`slack-cli` keeps one or more workspaces in
`~/.cli-tools/slack-cli/config.toml` (mode 0600). Get credentials in one of
these ways:

| Command | Source | Token |
| --- | --- | --- |
| `auth import` | Slack desktop app session | `xoxc-` + `d` cookie (`xoxd-`) |
| `auth slk` | tokens saved by [gammons/slk](https://github.com/gammons/slk) | `xoxc-` + cookie |
| `auth add` | a token you paste | `xoxp-`/`xoxb-` (OAuth), or `xoxc-` with `--cookie` |

`auth import` reads the session token from the app Local Storage and the `d`
cookie from the Cookies database, then decrypts the cookie with the OS keyring.
On macOS the system prompts once to let `slack-cli` read the "Slack Safe
Storage" key. Each workspace is saved by its domain.

A session token (`xoxc-`) can expire. `slack-cli` mints a fresh one from the
cookie when a call fails with an auth error, and saves it.

### Multiple workspaces

`auth import` saves every signed-in workspace. Pick a default:

```bash
slack-cli auth use acme
```

Override per call with `--workspace/-w`. The value matches a workspace name,
team id, domain, or team name:

```bash
slack-cli -w acme conversations list
slack-cli -w T0123ABCD chat post-message --channel '#ops' --text hi
```

### Token resolution order

For each value: **flag → environment variable → config file**.

| Variable | Use |
| --- | --- |
| `SLACK_WORKSPACE` | default workspace selector |
| `SLACK_CLI_CONFIG` | config file path |
| `SLACK_API_URL` | API root (for tests) |
| `SLACK_DEBUG` | print HTTP details to stderr |

## Commands

Each API noun is a command group; each method is a leaf. Method names become
kebab-case leaves (`chat.postMessage` → `chat post-message`). Short aliases
exist for common nouns (`conv`/`channel` for `conversations`, `msg` for `chat`,
`react` for `reactions`).

```bash
slack-cli --help                       # list all groups
slack-cli conversations --help         # list conversation methods
slack-cli chat post-message --help     # flags for one method
```

### Generic method flags

Every method leaf takes typed flags from its parameters, plus:

| Flag | Use |
| --- | --- |
| `--data`/`-d` | request body as JSON (`@file` or `-` for stdin); flags merge over it |
| `--set key=value` | set one body field (repeatable) |
| `--all`/`-a` | follow cursor pagination and return every item |
| `--max` | with `--all`, stop after this many items |
| `--dry-run` | print the request, send nothing |

Nested objects (`blocks`, `attachments`) are JSON strings, because the Slack
transport is form-urlencoded. Pass them as a JSON value:

```bash
slack-cli chat post-message --channel '#general' \
  --blocks '[{"type":"section","text":{"type":"mrkdwn","text":"*hi*"}}]'
```

### Name resolution

Flags that take a channel or user accept an id, `#channel`, `@user`, or an
email. `slack-cli` resolves the name to an id with a cached directory. Turn it
off with `--no-resolve`.

### Helper commands

Hand-written leaves for common tasks:

| Command | Use |
| --- | --- |
| `files upload <path>` | upload a file (the current external-upload flow) |
| `conversations read <channel>` | print recent messages with names resolved |
| `conversations find [query]` | find channels by name |
| `users find [query]` | find users by name or email |
| `canvases read <id>` | print a canvas as Markdown |

### Raw passthrough

Call any method directly:

```bash
slack-cli api conversations.history --param channel=C0123 --param limit=5
slack-cli api users.list --all
```

## listen

`slack-cli listen` streams events and prints one JSON object per line. It picks
a transport per workspace from the token:

| Token | Transport | Events |
| --- | --- | --- |
| `xoxc-` (session) | the desktop client WebSocket | everything the user sees |
| `xapp-` (app) | Socket Mode | what the app subscribes to |
| `xoxb-`/`xoxp-` (OAuth) | polls `conversations.history` | needs `--channel` |

For Socket Mode, add an app-level token with `auth add --app-token xapp-...`.

A session socket subscribes to channels lazily, like the Slack app, so a quiet
channel can start streaming only after it is used. `--eager` drops that, so the
server pushes events for every channel at once. Use it when you want full
coverage from the first second.

Filter and control the stream:

```bash
slack-cli listen --channel '#general' --channel '#ops'
slack-cli listen --mentions                 # only messages that mention you
slack-cli listen --dm                        # only direct messages
slack-cli listen --type message --no-self    # only messages, not your own
slack-cli listen --all-workspaces            # merge every workspace
slack-cli listen --eager                     # session socket: every channel at once
slack-cli listen --count 10                  # stop after 10 events
slack-cli listen --timeout 30s               # stop after 30 seconds
slack-cli listen --raw                       # emit the raw event object
```

Each normalized line has `workspace`, `team_id`, `type`, `subtype`, `channel`,
`user`, `ts`, `thread_ts`, `text`, `time`, and `raw`.

## Trebi connector

`slack-cli serve` is a [Trebi](https://trebi.ai) connector. It speaks the
trebi-connector/1 protocol on stdin and stdout, and writes logs to stderr. It
sends these events: `message`, `mention`, `message.edited`, `message.deleted`,
`reaction`, and `channel.join`. It does not send messages.

`SLACK_WORKSPACES` (or `--workspaces`) selects the workspaces: a
comma-separated list of names, team ids, or domains. `all` selects every
workspace. An empty value selects the default workspace. Each workspace needs a
session token (`auth import`) or an app token.

```bash
SLACK_WORKSPACES=big-time-data,nanit slack-cli serve
slack-cli serve --sandbox    # a fake Slack in the process, for conformance checks
```

Two read-only operations read one JSON object on stdin and write one JSON
object. The messages have the same shape as the events:

```bash
echo '{"query":"deploy in:#ops","limit":10}' | slack-cli op search
echo '{"room":"#general","limit":20}' | slack-cli op history
echo '{"room":"T0123/C0456","thread":"1767225600.000100"}' | slack-cli op history
```

`scripts/build.sh` also makes `build/slack-op`, a link to `slack-cli`:
`slack-op search` is `slack-cli op search`. The operations use it, because Trebi
does not let an operation binary also be a run command.

`trebi/trebi-connection.yaml` is the connection for this computer
(`from: inline`). Add it with
`trebi connector apply --file trebi/trebi-connection.yaml`.

## Output

Default output is a table. Use `--json`/`-j` for JSON, `--raw` for the full
Slack response, and `--columns/-c` to pick table columns (dotted paths allowed).

## Tests

```bash
go test ./...
```

Tests use an `httptest` server and local crypto; they make no live Slack calls.
The Trebi conformance check runs `slack-cli serve --sandbox`:

```bash
dir=$(mktemp -d) && mkdir "$dir/skill" && cp SKILL.md "$dir/skill/"
sed -n '/^manifest:/,$p' trebi/trebi-connection.yaml | tail -n +2 | sed 's/^  //' > "$dir/trebi-connector.yaml"
trebi connector conformance --manifest "$dir"
```
