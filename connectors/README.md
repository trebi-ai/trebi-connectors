# connectors

The source of the programs that the catalog builds and hosts. `whatsapp-cli` and `discord-cli` are channels: they send and receive messages. `github-cli`, `linear-cli`, `notion-cli`, and `microsoft-todo-cli` watch things: they have subscriptions and webhooks, and send no messages. Each one is a normal CLI with its own commands, plus a `serve` command that speaks `trebi-connector/1` on stdin and stdout. The Trebi daemon starts `serve` from the `events.command` of the catalog entry. The SDK in `../sdk` does the protocol work. The skill of each CLI is in its catalog entry.

| CLI | Module | Catalog entry |
|---|---|---|
| `whatsapp-cli` | `github.com/trebi-ai/trebi-connectors/connectors/whatsapp-cli` | `catalog/whatsapp` |
| `discord-cli` | `github.com/trebi-ai/trebi-connectors/connectors/discord-cli` | `catalog/discord` |
| `github-cli` | `github.com/trebi-ai/trebi-connectors/connectors/github-cli` | `catalog/github` |
| `linear-cli` | `github.com/trebi-ai/trebi-connectors/connectors/linear-cli` | `catalog/linear` |
| `notion-cli` | `github.com/trebi-ai/trebi-connectors/connectors/notion-cli` | `catalog/notion` |
| `microsoft-todo-cli` | `github.com/trebi-ai/trebi-connectors/connectors/microsoft-todo-cli` | `catalog/microsoft-todo` |

Each CLI has `scripts/build.sh [version] [output]`. The catalog workflow runs it on each platform. The root `go.work` builds each CLI against the SDK in the same commit.

## serve

- `<cli> serve` runs the adapter. Stdout belongs to the protocol. Logs go to stderr.
- `<cli> serve --sandbox` runs the same adapter over a fake service in the same process, with no account. `scripts/validate.sh --conformance` checks it in CI.
- State, cache, settings, and the two modes: each program follows "Adapter folder contract" in [`../CLAUDE.md`](../CLAUDE.md#adapter-folder-contract). The README of each CLI has "Use on its own" and "Use with Trebi".

## whatsapp-cli

- Login: `setup.login: [qr]`. The QR code of each attempt is one `qr` step. The drawer shows it.
- Events: `message`, `reaction`. A message that you send from the phone is an event with `sender.self: true`.
- Features: `rooms.list`, `rooms.open`, `history`, `replay`, `typing`, `seen`, `reactions`, `attachments.in`, `attachments.out`, `replies`.
- `text` is the message text, the caption of a file, or the text of a button or list reply. The store keeps `reply_to_id`, so `history` and `replay` keep the reply chain.
- Limits: `max_text` 65536, formats `[text]`.
- `serve` holds the store lock and the WhatsApp connection. The send commands of the CLI forward to it over the store socket, as they do with `listen`.
- `seen` sends a read receipt. `typing` sends the composing state.
- `history` and `replay` read the local store. `replay` returns `complete: false` when the cursor id is not in the store.
- Inbound attachments up to 100 MiB are downloaded into the store. The event gives the absolute local `path`.

Features that it does not have:

- `threads` and `threads.create`: WhatsApp has no threads. A reply is `reply_to`. The daemon follows `reply_to` to the root of the conversation.
- `edit`: whatsmeow can send an edit, but the store has no edit history and the plan does not ask for it. Add it when a use needs it.

Warning: whatsapp-cli uses the WhatsApp Web protocol. WhatsApp can ban an account that uses it.

## discord-cli

- Token: the `DISCORD_TOKEN` input. There is no interactive login. A missing token gives status `auth_required` with reason `missing_input`. A revoked token gives reason `revoked`.
- The bot needs the Message Content intent in the Discord developer portal. Without it, the gateway closes with code 4014, and the status is `error` with a message that tells you to turn it on.
- Events: `message`, `reaction`. A message in a thread has the parent channel as `room` and the thread as `thread`.
- Features: `rooms.list`, `rooms.open`, `threads`, `threads.create`, `history`, `replay`, `typing`, `seen`, `reactions`, `edit`, `attachments.in`, `attachments.out`, `replies`.
- `text` is the content, then the readable parts of each embed: author, title, description, each field as `<name>: <value>`, and footer. `data.embeds` has the full embeds. `sender.bot` is true for a bot or a webhook.
- Limits: `max_text` 2000, formats `[markdown]`.
- `seen`: Discord has no read receipt for bots. `seen` adds the 👀 reaction.
- `replay` covers messages only, not reactions. It reads the channels and the active threads that had activity after the cursor. It reads a DM only when the DM is known: a bot cannot list its DMs, so the adapter keeps each DM that it sees in its state.
- `rooms.list` lists the guild text and announcement channels and the known DMs.
- Outbound attachments are at most 25 MiB each.

## Connectors that watch things

These four CLIs have the `subscriptions` feature, and the `webhooks` feature for a connection with a hosted hook (Trebi Cloud). Without a hook, each one checks the platform at an interval and reports the mode `poll`. `CONTRIBUTING.md` has the rules in "Watch things".

| CLI | Login | Form | Modes | Signature |
|---|---|---|---|---|
| `github-cli` | `device_code`, or the "Personal access token" input | `repository` (dynamic, room) | `api`, `poll` | `x-hub-signature-256` with the hook secret |
| `linear-cli` | the "API key" input | `team` (dynamic, room), `resources` (choices, multiple) | `api`, `poll` | `linear-signature` with the webhook secret |
| `notion-cli` | the "Integration secret" input | none | `manual`, `poll` | `x-notion-signature` with the verification token |
| `microsoft-todo-cli` | `device_code` | `list` (dynamic, room) | `api`, `poll` | `clientState` in the body, with a `validationToken` handshake |

## github-cli

- Login: the device flow with the Trebi GitHub OAuth app, scopes `repo` and `admin:repo_hook`. The build sets the app id with `TREBI_GITHUB_CLIENT_ID`. The token goes to `auth.json` in the state folder. The "Personal access token" input wins over the login.
- Events: `push`, `pull_request`, `pull_request_review`, `issues`, `issue_comment`, `release`, `workflow_run`. The room is the repository. The thread of an issue or a pull request is its number.
- Sync with a hook: one GitHub webhook for each repository, with the union of the event types of its subscriptions. The adapter keeps its hooks in `github-hooks.json` and deletes the ones that no subscription uses. 403 or 404 gives the state `error` with "Trebi needs admin access to <repo>".
- Sync without a hook: the adapter reads `GET /repos/{repo}/events` with `If-None-Match`, at least every 60 s. The first read only finds the position. A poll event id starts with `poll:`.
- `webhook/receive` checks `x-hub-signature-256` against the current secret and the last three secrets. A `ping` gives no event.

## linear-cli, notion-cli, and microsoft-todo-cli

The README of each CLI has the details: the GraphQL webhook of Linear, the manual setup of a Notion integration webhook, and the Graph subscriptions of Microsoft To Do, which expire and renew.

## Build and test

```bash
cd connectors/discord-cli && go test ./...
cd connectors/whatsapp-cli && CGO_ENABLED=1 go test -tags sqlite_fts5 ./...
cd connectors/github-cli && go test -race ./...
```

## Release

Bump the `version` of the catalog entry in the same PR as the CLI change. The catalog workflow builds and publishes the program when the PR merges. See "Programs from `connectors/`" in `../README.md`.
