# 🗃️ whatsapp-cli — WhatsApp CLI: sync, search, send.

WhatsApp CLI built on top of `whatsmeow`, focused on:

- Best-effort local sync of message history + continuous capture
- Fast offline search
- Sending messages
- Contact + group management

This is a third-party tool that uses the WhatsApp Web protocol via `whatsmeow` and is not affiliated with WhatsApp.

## Status

Core implementation is in place. See `docs/spec.md` for the full design notes.

## Recent updates (0.2.0)

- Messages: search/list includes display text for reactions, replies, and media types.
- Send: `whatsapp-cli send file --filename` to override the display name.
- Auth: optional `WHATSAPP_CLI_DEVICE_LABEL` / `WHATSAPP_CLI_DEVICE_PLATFORM` env overrides.

## Install / Build

Choose **one** of the following options.  
If you install via Homebrew, you can skip the local build step.

### Option A: Install via Homebrew (tap)

- `brew install steipete/tap/whatsapp-cli`

### Option B: Build locally

- `go build -tags sqlite_fts5 -o ./dist/whatsapp-cli ./cmd/whatsapp-cli`

Run (local build only):

- `./dist/whatsapp-cli --help`

## Quick start

Default store directory is `~/.whatsapp-cli` (override with `--store DIR`).

```bash
# 1) Authenticate (shows QR), then bootstrap sync
pnpm whatsapp-cli auth
# or: ./dist/whatsapp-cli auth (after pnpm build)

# 2) Keep syncing (never shows QR; requires prior auth)
pnpm whatsapp-cli sync --follow

# Diagnostics
pnpm whatsapp-cli doctor

# Search messages
pnpm whatsapp-cli messages search "meeting"

# Backfill older messages for a chat (best-effort; requires your primary device online)
pnpm whatsapp-cli history backfill --chat 1234567890@s.whatsapp.net --requests 10 --count 50

# Download media for a message (after syncing)
./whatsapp-cli media download --chat 1234567890@s.whatsapp.net --id <message-id>

# Send a message
pnpm whatsapp-cli send text --to 1234567890 --message "hello"

# Send a file
./whatsapp-cli send file --to 1234567890 --file ./pic.jpg --caption "hi"
# Or override display name
./whatsapp-cli send file --to 1234567890 --file /tmp/abc123 --filename report.pdf

# List groups and manage participants
pnpm whatsapp-cli groups list
pnpm whatsapp-cli groups rename --jid 123456789@g.us --name "New name"
```

## Prior Art / Credit

This project is heavily inspired by (and learns from) the excellent `whatsapp-cli` by Vicente Reig:

- [`whatsapp-cli`](https://github.com/vicentereig/whatsapp-cli)

## High-level UX

- `whatsapp-cli auth`: interactive login (shows QR code), then immediately performs initial data sync.
- `whatsapp-cli sync`: non-interactive sync loop (never shows QR; errors if not authenticated).
- Output is human-readable by default; pass `--json` for machine-readable output.

## Use on its own

With no `TREBI_STATE_DIR` in the env, `whatsapp-cli` is a normal CLI. It finds the store folder in this order:

1. The `--store DIR` flag.
2. The env var `WHATSAPP_CLI_STORE_DIR`.
3. `~/.whatsapp-cli`.

These env vars set the linked device:

- `WHATSAPP_CLI_DEVICE_LABEL`: the device name that the phone shows.
- `WHATSAPP_CLI_DEVICE_PLATFORM`: the device platform (`CHROME` when it is not set or not valid).

## Use with Trebi

Trebi starts `whatsapp-cli serve` with `TREBI_STATE_DIR` set. This is Trebi mode. The Trebi values win over everything:

- The store is `TREBI_STATE_DIR`. The CLI does not read `WHATSAPP_CLI_STORE_DIR` or the home folder.
- `--store` fails with "Trebi sets this value".
- The device name comes from the `WA_DEVICE_NAME` input of the connection. The CLI does not read `WHATSAPP_CLI_DEVICE_LABEL` or `WHATSAPP_CLI_DEVICE_PLATFORM`.
- A CLI command in a Trebi run gets the same `TREBI_STATE_DIR`. A send forwards to the running `serve` over the store socket.
- `serve --sandbox` runs the same adapter over a fake WhatsApp. It needs no account. The login is a QR step that completes by itself. Outside Trebi, the sandbox uses a temp store unless you give `--store`.

The rules for all connector programs are in "Adapter folder contract" in `../../CLAUDE.md`.

## Storage

The store holds `session.db`, `whatsapp-cli.db`, `media/`, `LOCK`, and the socket `whatsapp-cli.sock`.

- Media paths in `whatsapp-cli.db` are relative to the store, so you can move the folder. A path from `media download --output` outside the store stays absolute.
- `whatsapp-cli.db` has a schema version (`PRAGMA user_version`). A newer program upgrades an older store when it opens it. An older program does not open a newer store: `serve` then sends `status` `error` and changes nothing.
- On a clean stop, the CLI moves the SQLite WAL into each database file and closes it.
- At start, the CLI deletes the download temp files (`.whatsapp-cli-download-*`) that a crash left.

## Backfilling older history

`whatsapp-cli sync` stores whatever WhatsApp Web sends opportunistically. To try to fetch *older* messages, use on-demand history sync requests to your **primary device** (your phone).

Important notes:

- This is **best-effort**: WhatsApp may not return full history.
- Your **primary device must be online**.
- Requests are **per chat** (DM or group). `whatsapp-cli` uses the *oldest locally stored message* in that chat as the anchor.
- Recommended `--count` is `50` per request.

### Backfill one chat

```bash
pnpm whatsapp-cli history backfill --chat 1234567890@s.whatsapp.net --requests 10 --count 50
```

### Backfill all chats (script)

This loops through chats already known in your local DB:

```bash
pnpm -s whatsapp-cli -- --json chats list --limit 100000 \
  | jq -r '.[].JID' \
  | while read -r jid; do
      pnpm -s whatsapp-cli -- history backfill --chat "$jid" --requests 3 --count 50
    done
```

## License

See `LICENSE`.

## Maintainers
- Created by [@steipete](https://github.com/steipete)
- Currently maintained by [@dinakars777](https://github.com/dinakars777)
