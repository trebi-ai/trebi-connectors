# src

The source of the programs that the catalog builds and hosts. Each one is a normal CLI with its own commands, plus a `serve` command that speaks `trebi-connector/1` on stdin and stdout. The Trebi daemon starts `serve` from the `events.command` of the catalog entry. The SDK in `../sdk` does the protocol work. The skill of each CLI is in its catalog entry.

| CLI | Module | Catalog entry |
|---|---|---|
| `whatsapp-cli` | `github.com/trebi-ai/trebi-connectors/src/whatsapp-cli` | `catalog/whatsapp` |
| `discord-cli` | `github.com/trebi-ai/trebi-connectors/src/discord-cli` | `catalog/discord` |

Each CLI has `scripts/build.sh [version] [output]`. The catalog workflow runs it on each platform. The root `go.work` builds each CLI against the SDK in the same commit.

## serve

- `<cli> serve` runs the adapter. Stdout belongs to the protocol. Logs go to stderr.
- `<cli> serve --sandbox` serves in-memory rooms with no account. The daemon conformance checks use it in CI.
- The daemon sets `TREBI_STATE_DIR`. The SDK keeps its send dedupe file there.

## whatsapp-cli

- Login: `setup.login: [qr]`. The QR code of each attempt is one `qr` step. The drawer shows it.
- Session store: `$TREBI_STATE_DIR` when it is set, else `$WHATSAPP_CLI_STORE_DIR`, else `~/.whatsapp-cli`. The store holds `session.db`, `whatsapp-cli.db`, and `media/`.
- Events: `message`, `reaction`. A message that you send from the phone is an event with `sender.self: true`.
- Features: `rooms.list`, `rooms.open`, `history`, `replay`, `typing`, `seen`, `reactions`, `attachments.in`, `attachments.out`.
- Limits: `max_text` 65536, formats `[text]`.
- `serve` holds the store lock and the WhatsApp connection. The send commands of the CLI forward to it over the store socket, as they do with `listen`.
- `seen` sends a read receipt. `typing` sends the composing state.
- `history` and `replay` read the local store. `replay` returns `complete: false` when the cursor id is not in the store.
- Inbound attachments up to 100 MiB are downloaded into the store. The event gives the local `path`.

Features that it does not have:

- `threads` and `threads.create`: WhatsApp has no threads. A reply is `reply_to`.
- `edit`: whatsmeow can send an edit, but the store has no edit history and the plan does not ask for it. Add it when a use needs it.

Warning: whatsapp-cli uses the WhatsApp Web protocol. WhatsApp can ban an account that uses it.

## discord-cli

- Token: `DISCORD_TOKEN`, then `DISCORD_BOT_TOKEN`, then the config file of `discord-cli auth set`. There is no interactive login. A revoked token gives status `auth_required` with reason `revoked`.
- The bot needs the Message Content intent in the Discord developer portal. Without it, the gateway closes with code 4014, and the status is `error` with a message that tells you to turn it on.
- Events: `message`, `reaction`. A message in a thread has the parent channel as `room` and the thread as `thread`.
- Features: `rooms.list`, `rooms.open`, `threads`, `threads.create`, `history`, `replay`, `typing`, `seen`, `reactions`, `edit`, `attachments.in`, `attachments.out`.
- Limits: `max_text` 2000, formats `[markdown]`.
- `seen`: Discord has no read receipt for bots. `seen` adds the 👀 reaction.
- `replay` covers messages only, not reactions. It reads the channels and the active threads that had activity after the cursor. It reads a DM only when the DM is known: a bot cannot list its DMs, so the adapter keeps each DM that it sees in `$TREBI_STATE_DIR/discord-dms.json`.
- `rooms.list` lists the guild text and announcement channels and the known DMs.
- Outbound attachments are at most 25 MiB each.

## Build and test

```bash
cd src/discord-cli && go test ./...
cd src/whatsapp-cli && CGO_ENABLED=1 go test -tags sqlite_fts5 ./...
```

## Release

Bump the `version` of the catalog entry in the same PR as the CLI change. The catalog workflow builds and publishes the program when the PR merges. See "Programs from `src/`" in `../README.md`.
