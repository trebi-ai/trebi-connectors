# connectors

The source of the programs that the catalog builds and hosts. Each one is a normal CLI with its own commands, plus a `serve` command that speaks `trebi-connector/1` on stdin and stdout. The Trebi daemon starts `serve` from the `events.command` of the catalog entry. The SDK in `../sdk` does the protocol work. The skill of each CLI is in its catalog entry.

| CLI | Module | Catalog entry |
|---|---|---|
| `whatsapp-cli` | `github.com/trebi-ai/trebi-connectors/connectors/whatsapp-cli` | `catalog/whatsapp` |
| `discord-cli` | `github.com/trebi-ai/trebi-connectors/connectors/discord-cli` | `catalog/discord` |

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

## Build and test

```bash
cd connectors/discord-cli && go test ./...
cd connectors/whatsapp-cli && CGO_ENABLED=1 go test -tags sqlite_fts5 ./...
```

## Release

Bump the `version` of the catalog entry in the same PR as the CLI change. The catalog workflow builds and publishes the program when the PR merges. See "Programs from `connectors/`" in `../README.md`.
