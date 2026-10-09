# Contributing a connector

This guide tells you how to add a connector to the Trebi catalog or change one. A connector connects Trebi to one external system. It can give three things:

- **Actions.** Commands or MCP tools that an agent uses in a run.
- **Events.** Things that happen in the system and start a job, for example a new message.
- **A channel.** A conversation: Trebi reads messages and sends replies.

Trebi reviews every entry before it goes into the catalog. You open a pull request. A maintainer reviews it and approves the CI run. CI then signs and publishes the entry to `catalog.trebi.ai`.

`README.md` has the publish flow. `CLAUDE.md` has the rules for an entry and the adapter folder contract. `sdk/README.md` has the Go SDK. `.claude/skills/connector-authoring/SKILL.md` has the full steps to build a program with `serve`.

## Choose a path

| Path | What you write | Example |
|---|---|---|
| A. Actions only | A manifest and a skill. The CLI or MCP server comes from another place. | `catalog/apollo`, `catalog/google` |
| B. Actions and events | Path A, plus a program that sends events to Trebi. | `catalog/github`, `catalog/linear`, `catalog/notion`, `catalog/microsoft-todo` |
| C. Channel | Path B, plus the channel features: rooms, history, replies. | `catalog/discord`, `catalog/whatsapp` |

Start with path A. Most connectors need only actions. Add events when a user must start a job from something that happens in the system. Add a channel when a user must talk with an agent through the system.

## The pull request

1. Fork the repository and make a change on your fork.
2. Add the folder `catalog/<name>/`. The folder name is the `name` field of the manifest. It matches `^[a-z0-9][a-z0-9-]{0,39}$`. The names `email`, `webhook`, and `trebi` are reserved.
3. Write `catalog/<name>/trebi-connector.yaml` and `catalog/<name>/skill/SKILL.md`.
4. Set `publisher: community`. Only an entry that Trebi maintains has `publisher: trebi`.
5. Run `scripts/validate.sh <name>`. For an entry with events, also run `scripts/validate.sh --conformance <name>`.
6. Open the pull request against `main`. Fill in the template: what the connector does, how you tested it, and which account type you used. "Testing" tells how to test against the real system.

A change to an entry that exists must bump `version` in the same pull request. A published version is immutable.

A community pull request normally changes only `catalog/`. A maintainer owns `sdk/`, `connectors/`, `schema/`, `tools/`, `scripts/`, and `.github/` (see `.github/CODEOWNERS`). If your connector needs a program in `connectors/`, open an issue first. Describe the program, and a maintainer tells you how to continue.

Write all prose in ASD-STE100 Simplified Technical English: short sentences, active voice, present tense. Write one paragraph per line, with no hard wraps.

## Path A: actions only

### The manifest

```yaml
schema: trebi-connector/1
name: example
title: "Example"
description: "Read and update tickets in Example."
version: 0.1.0
publisher: community
homepage: https://example.com
license: MIT
platforms: [darwin/arm64, darwin/amd64, linux/arm64, linux/amd64]
install:
  github_release:
    repo: example/example-cli
    tag: "v{version}"
    asset: "example-cli_{version}_{os}_{arch}.tar.gz"
    checksums: "checksums.txt"
  bin: example-cli
  version_command: example-cli --version
setup:
  inputs:
    - name: EXAMPLE_API_KEY
      label: "API key"
      help: "Create an API key in Example under Settings, API."
      url: "https://example.com/settings/api"
      secret: true
      required: true
actions:
  cli: {commands: [example-cli]}
```

The schema is `schema/trebi-connector.schema.json`. The decoder is strict: an unknown key fails.

- `title` and `description` are for the user. Write `description` as one sentence that tells what the user can do.
- `actions` has `cli` or `mcp`. `cli.commands` lists the programs that an agent can call. `mcp` has exactly one of `command` (a local server), `url` (a remote server), or `name` with `registry`. `${NAME}` in `args`, `env`, `url`, and `headers` expands from the inputs.
- `warning` is an optional sentence that the user sees before the install, for example a ban risk.

### Install the program

Trebi installs the program of an entry with the first method of `install` that works. Each method needs `install.bin`.

| Method | Use it when | Notes |
|---|---|---|
| `github_release` | The program has releases on GitHub. | `tag` and `asset` take `{version}`, `{os}`, and `{arch}`. Give `checksums`: Trebi checks the asset against it. `scripts/validate.sh --check-assets` checks that the assets exist. |
| `brew` | The program has a Homebrew formula. | The value is the formula name. |
| `go` | The program installs with `go install`. | The value is the package path with a version. The user must have Go. |
| `catalog` | Trebi builds the program from `connectors/<bin>`. | Only for a program in this repository. A maintainer adds it. |

`version_command` prints the version of the program. Trebi runs it after the install to check the program.

An entry with no `install` block expects the program on the `PATH` of the user. The connector row then shows the command as missing until the user installs it. Use this only for a program that a user installs by hand.

### The CLI

Trebi does not require subcommands for actions. An agent reads the skill and calls the commands. A good CLI for an agent has these properties:

- It gives JSON output with a flag (for example `--json`), so an agent can parse it.
- It reads each credential from one env name. That env name is the `name` of an input.
- It writes errors to stderr and exits with a code that is not zero on failure.
- It needs no prompt from a person. An interactive prompt blocks the run.
- It has `--version`.

### Inputs

Each item of `setup.inputs` is one value that the user gives when they add a connection. Trebi puts the value in the env of the program under `name`.

| Field | Meaning |
|---|---|
| `name` | The env name. It matches `^[A-Za-z_][A-Za-z0-9_]*$`. |
| `label` | The name that the user sees, for example "API key". Never use the env name as the label. A required input must have a label. |
| `help` | One or two sentences that tell where to get the value. |
| `url` | The page where the user gets the value. |
| `secret` | `true` for a token, a key, or a password. Trebi keeps it in a secret file and masks it. |
| `required` | `true` when the connector cannot work without it. A missing required input stops the connection, and the user sees "\<label\> is missing". |
| `default`, `choices`, `format` | An optional default value, a closed list of values, and a format hint. |

### The skill

`skill/SKILL.md` teaches an agent to use the commands. It is the only copy of the skill. Trebi puts it in the run when a job names the connection.

```markdown
---
name: example-cli
description: Read and update tickets in Example with example-cli. Use when the user asks to find, create, or close an Example ticket.
---

# example-cli

## Find tickets
...
```

- The frontmatter must have `name` and `description`. `scripts/validate.sh` checks both.
- Show real commands with their flags and a short sample of the output.
- Tell the agent which commands change data, so it can ask before it uses them.
- Do not tell the agent how to set a credential. Trebi sets it.

## Auth

Choose one of two kinds of auth.

**Inputs only.** The user pastes an API key or a token into a form. Use `setup.inputs` with `secret: true`. This works for every path, also for path A. Most connectors use it.

**A login flow.** The user logs in through the UI: they scan a code, open a page, or type a code. Use `setup.login`. A login flow needs a program that speaks `trebi-connector/1` (path B or C), because the program runs the flow. List the step kinds that the program can start:

| Step kind | What the user sees | Example |
|---|---|---|
| `qr` | A QR code to scan with a phone. | WhatsApp linked device |
| `device_code` | A URL and a short code to type on that page. | Microsoft, GitHub device flow |
| `url` | A button that opens a page, for example an OAuth consent page. | OAuth with a redirect |
| `input` | A form with one or more fields, for example an SMS code. | A two-step code |
| `wait` | A message while the program waits for the system. | An approval on another device |

The flow on the wire:

1. The user clicks "Log in". Trebi sends `auth/begin` with a `kind`.
2. The program answers with the first `step`. It sends more steps with `auth/step` when the flow needs them.
3. For an `input` step, Trebi sends the values of the user with `auth/submit`.
4. The program sends `auth/done` with `ok` and the `account`, then `status` `connected`.

The Go SDK runs this for you when the adapter implements `sdk.Authenticator`. The fixtures in `sdk/contract/auth.*.json` and `sdk/contract/flow.login.qr.jsonl` show each message.

Rules for auth:

- Keep the login session and each token in `TREBI_STATE_DIR`. Refresh a token in the program. Do not ask the user to log in again for an expired access token.
- When a token cannot be refreshed, send `status` `auth_required`. Trebi then shows "Log in" to the user.
- When a required input is missing, return `sdk.MissingInput` from `Initialize`. Never look for the value in a file in the home folder.
- Ask only for the scopes that the connector uses. Tell the scopes in the skill.

## Path B: events

Events need a long-lived program that Trebi starts from `events.command`. Two protocols exist.

### Protocol `lines`

The program prints one event per line on stdout. Use it for a simple source that already exists, for example a CLI with a `watch` command.

```yaml
events:
  protocol: lines
  command: example-cli watch --json
  format: json
  id: id                 # the dotted path of the event id; Trebi drops a repeated id
  ts: created_at
  identity: author.email # the sender, for the trigger allowlist
  restart: on-failure
  types:
    - {type: ticket, description: A new ticket, schema: schemas/ticket.json}
```

- `poll` (at least `10s`) runs the command again at that interval, for a command that prints and exits.
- `catchup.command` prints the events since the last event when Trebi starts again. Trebi sets `TREBI_LAST_EVENT_ID` in its env. It needs `format: json` and `id`, and it cannot be combined with `poll`.
- `lines` has no login flow and no channel.

### Protocol `trebi-connector/1`

The program is a JSON-RPC 2.0 server on stdin and stdout, one message per line. Use it for a login flow, a channel, or rich typed events. Write it with the Go SDK. A program for this protocol must have these commands:

| Command | Purpose |
|---|---|
| `<cli> serve` | Speaks the protocol. Trebi starts it from `events.command`. |
| `<cli> serve --sandbox` | Runs the real adapter over a fake service, with no account and no network. The conformance check uses it. |
| `<cli> --version` | Prints the version. `install.version_command` uses it. |

The program obeys the adapter folder contract in `CLAUDE.md`: durable data in `TREBI_STATE_DIR`, temp data in `TREBI_CACHE_DIR`, logs to stderr, one writer, and a clean stop on `shutdown`.

### Write a good event

An event is a typed `event` message. `sdk/contract/event.message.json` is an example.

| Field | Rule |
|---|---|
| `id` | A stable id from the system, for example the message id or the delivery id. Trebi drops a second event with the same id, so a retry or a restart does not start a job two times. |
| `type` | One of the types in `events.types`. An undeclared type counts as a parse error. |
| `room` | Where the event happened: a chat, a channel, a repository, a list. A trigger can limit a job to some rooms. Use the same room id in every event of that room. |
| `thread` | The thread in the room, or null. |
| `sender` | `{id, name}` of the person or the bot. Set `bot: true` for a bot. Set `self: true` for the account itself: Trebi stores that event and does not start a job. |
| `text` | What a person sees, in plain text. Never put raw JSON here. |
| `data` | The structured content. Give each type a JSON Schema in `schemas/<type>.json` that describes `data`. |
| `reply_to` | The id of the message that this message answers, when there is one. |

Each schema file must be a valid JSON Schema (draft 2020-12). Give each property a `description`: an agent reads it to write a trigger.

### Watch things

A user often wants events about one thing in the system: a repository, a team, a list, or a workspace. Add `events.subscriptions` to let the user choose those things. The user then opens the connector, clicks the action (for example "Watch a repository"), fills a short form, and selects the event types. Trebi gets the events by the best method that is available.

```yaml
events:
  protocol: trebi-connector/1
  command: linear-cli serve
  restart: always
  types:
    - {type: issue, description: An issue created, changed, or removed, schema: schemas/issue.json}
    - {type: comment, description: A comment on an issue, schema: schemas/comment.json}
  subscriptions:
    action: Watch a team
    modes: [api, poll]
    fields:
      - name: team
        label: Team
        help: Leave it empty to watch every public team.
        dynamic: true
        room: true
      - name: resources
        label: Changes to
        choices: [Issue, Comment, Project, Cycle]
        multiple: true
        default: Issue
    webhook:
      headers: [linear-signature, linear-delivery, linear-event]
```

- `subscriptions` needs `protocol: trebi-connector/1`. The program implements the SDK interfaces in `sdk/README.md` ("Watch things").
- `action` is the verb on the button, at most 40 characters. Write it for the user: "Watch a repository", not "Create webhook".
- `fields` is the form, at most 8 fields. A field has a `label`. It has `choices` (a closed list), `dynamic: true` (the program loads the options), or neither (free text). `multiple: true` takes a list. A connector with no fields shows one switch.
- At most one field has `room: true`. Its value is the `room` of each event, so a trigger can limit a job to that thing. A room field is not `multiple`.
- `webhook.headers` lists the headers that the program reads, for example the signature and the delivery id. Trebi forwards only these headers and a short default list. These names are refused: `authorization`, `cookie`, `set-cookie`, `proxy-authorization`, and each name that starts with `cf-` or `x-forwarded-`.
- `webhook.handshake` lists the handshake rules of the system. See below.

#### The four modes

`modes` lists the methods that are possible. The program chooses the mode of each subscription when Trebi syncs the list. The login and the Trebi Cloud link of the user decide it.

| Mode | When to use it | What the user sees |
|---|---|---|
| `api` | The program can register a webhook through the API of the system. | "Live" |
| `manual` | A person must register the webhook on a settings page of the system. The program shows the URL to copy and asks for the values to paste, for example a signing secret. | "Finish the setup", then "Live" |
| `stream` | The system has a socket or a long poll that the program holds open. | "Live" |
| `poll` | The program reads the changes at an interval. | "Checks every few minutes" |

`api` and `manual` need the `webhook` block. They also need a Trebi Cloud link, because the cloud hosts the URL. Without a link, the program has no webhook URL. Always list `poll` (or `stream`) too, so that the connector works for a user with no link. This is the poll fallback. The program then polls at an interval that the system allows, at least one minute. A switch between a webhook and a poll can repeat one change with another event id. Tell this in the skill.

#### Handshake rules

Some systems check the URL before they send events. The cloud answers these checks for the program, from the rules in `webhook.handshake`. It tries the rules in order, and the first match answers.

| Rule | Matches when | The cloud answers | The program gets it |
|---|---|---|---|
| `query_echo:<param>` | the query has `<param>` | 200 `text/plain` with the value | yes, with `handshake: true` |
| `json_echo:<field>` | the body is a JSON object with a string `<field>`, and it has no other key except `type` and `token` | 200 `application/json` `{"<field>": value}` | yes |
| `header_echo:<header>` | the request has `<header>` | 200, the same header with the same value | yes |
| `ok:GET`, `ok:HEAD` | the method is `GET` or `HEAD` and no echo rule matched | 200, no body | no |

For example, Microsoft Graph uses `query_echo:validationToken`, Slack and Monday use `json_echo:challenge`, and Asana uses `header_echo:x-hook-secret`. A request that matches no rule is a normal delivery. It must use `POST`, `PUT`, or `PATCH`. A check that needs the app secret of the system (Zoom, X) is not supported.

#### Auth scopes for webhooks

A webhook needs more access than a read. Ask for the scope that lets the program create and delete webhooks, and tell it in the skill. For example, GitHub needs `admin:repo_hook`, and a fine-grained token needs "Webhooks: read and write". When the user cannot give that access, the program returns the state `error` with a short message, for example "Trebi needs admin access to octo/app". It never asks for more scopes than the connector uses.

#### Rules for the program

- Check the signature of each delivery. A bad signature returns `invalid`. Trebi drops it.
- Keep the platform ids and the platform secrets in `TREBI_STATE_DIR`. A restore can give a new URL. The next sync then deletes the old webhooks and makes new ones.
- Read a thin payload (an id only) with the API before you send the event.
- Write each `action` text in simple words for a person who does not know webhooks.
- The conformance check runs `serve --sandbox`: the fake system of the program must register a webhook and send one signed delivery.

## Path C: a channel

A channel adds the `channel` block. Each feature in `channel.features` must match a method that the adapter serves, and `channel.limits` must match what `initialize` returns. The adapter must not promise more than the manifest. The feature list and the SDK interface of each feature are in `sdk/README.md`. A channel CLI also has a history command that reads the earlier messages of a room. The skill shows it in a section "Read a conversation".

## Checks before you open the pull request

- [ ] The folder name equals `name`.
- [ ] `version` is new for a change to an entry that exists.
- [ ] Each required input has a `label`, and no label is an env name.
- [ ] Each secret input has `secret: true`.
- [ ] `skill/SKILL.md` has `name` and `description`, and its commands work.
- [ ] Each event type has a schema in `schemas/`.
- [ ] An entry with `events.subscriptions` lists `poll` or `stream` in `modes`, so it works with no Trebi Cloud link.
- [ ] The program needs no prompt from a person and writes no file outside the two Trebi folders.
- [ ] `scripts/validate.sh <name>` passes.
- [ ] For an entry with events or a channel, `scripts/validate.sh --conformance <name>` passes. It needs a `trebi` on `PATH` that has the `connector conformance` command.

## Testing

Two levels of tests exist. CI runs the first level on each pull request. You run the second level on your own machine.

### Tests in CI, with no credentials

These tests must pass before a merge. They need no account and no secret:

- `scripts/validate.sh <name>` checks the manifest, the skill, and the schemas.
- The unit tests of a program in `connectors/` run each command against the fake system of the program.
- `scripts/validate.sh --conformance <name>` runs `serve --sandbox` against the same fake system.

The fake system is the contract of the program. Make it behave like the real system: the same paths, the same payload forms, and the same errors.

### Live tests, on your machine

CI never has credentials for a live system. GitHub gives no secrets to a pull request from a fork, and Trebi does not put test accounts in this repository. Do not add a workflow that sends a secret to the code of a pull request.

Test against the real system with your own account:

1. Build the program, or install the CLI of the entry.
2. Log in with your own account. Use a test account or a test workspace when the system has one.
3. Run each command that the pull request adds or changes. For an entry with events, start `serve` and make each event happen one time.
4. Write the result in the pull request: the account type, the trebi version, each command, and what it did. The template has the fields.
5. When the real system does not do what the docs say, change the fake system to do the same, and add a test. The difference is then checked on each pull request.

Do not put a token, a key, a password, or personal data in the pull request, in the test data, or in the output that you paste. Replace ids and email addresses with examples.

A maintainer reads your result and can run the live tests again before the merge. A live test is not a merge gate. A pull request with good fake-system tests and a clear live result can merge when CI passes.

## What CI does

A pull request runs `.github/workflows/catalog.yml`. It validates every entry and runs the conformance check with the newest `trebi` release on each entry that the pull request changes. A pull request from a fork runs only after a maintainer approves the run. CI does not have the CLIs that the catalog does not build, so it skips the `actions.cli` suite for those entries. Run the full check on your machine with the CLI installed. When the run cannot download `trebi`, it skips the conformance check and shows a notice. A maintainer then runs the check before the merge. After the merge, CI signs the index and publishes the entry. A user then finds it in the catalog in the Trebi app.
