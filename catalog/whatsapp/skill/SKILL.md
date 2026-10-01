---
name: whatsapp-cli
description: >
  Interact with WhatsApp via whatsapp-cli (WhatsApp Web protocol / whatsmeow).
  Use when the user wants to send WhatsApp messages (text or files), sync/fetch
  latest messages, search message history, manage groups/chats/contacts, download
  media, backfill older history, or stream live events. Authenticates once via QR;
  session + local SQLite store under ~/.whatsapp-cli, or in the connection folder in Trebi.
  Triggers: "send WhatsApp", "check my WhatsApp", "sync WhatsApp", "latest messages",
  "WhatsApp group", "message someone on WhatsApp", "pull WhatsApp history", "whatsapp-cli".
---

# whatsapp-cli — WhatsApp CLI Skill

Go CLI (`cobra`) wrapping the WhatsApp Web protocol via
[whatsmeow](https://github.com/tulir/whatsmeow). Binary: **`whatsapp-cli`**. Local
SQLite sync, offline FTS search, send text/files, group management, and live event
streaming — no re-QR after the first `whatsapp-cli auth`.

Source: `connectors/whatsapp-cli/` in `github.com/trebi-ai/trebi-connectors`.
Build: `scripts/build.sh` → `build/whatsapp-cli` (requires **CGO** + `-tags sqlite_fts5`).
Not affiliated with WhatsApp.

## When to use

- Send a message or file on WhatsApp
- Check latest messages, search history, or sync state
- Create/manage groups, list chats/contacts
- One-shot sync (pull messages and exit)
- Stream live events (`listen`) for long-running agents

## Prerequisites

- Binary installed: `whatsapp-cli` on PATH (Trebi installs it from the catalog, or run `connectors/whatsapp-cli/scripts/build.sh`)
- Initial auth: `whatsapp-cli auth` (scan QR once)
- Store directory: `~/.whatsapp-cli` (override with `--store DIR` or `WHATSAPP_CLI_STORE_DIR`). In Trebi, the connection sets the store. Do not pass `--store` in a Trebi run: it fails with "Trebi sets this value".
- Prefer `--json` when parsing output

## Global flags

Place global flags before the subcommand (cobra style also accepts them after):

| Flag | Meaning |
| --- | --- |
| `--store DIR` | Store directory (default `$WHATSAPP_CLI_STORE_DIR` or `~/.whatsapp-cli`) |
| `--json` | Machine-readable JSON on stdout |
| `--timeout D` | Timeout for non-sync commands (default `5m`) |
| `-v` / `--version` | Print version |

Env on its own: `WHATSAPP_CLI_STORE_DIR`, `WHATSAPP_CLI_DEVICE_LABEL`, `WHATSAPP_CLI_DEVICE_PLATFORM`. In Trebi (`TREBI_STATE_DIR` is set), the CLI reads none of them: the store is `TREBI_STATE_DIR` and the device name is the `WA_DEVICE_NAME` input.

## Auth & connection

```bash
whatsapp-cli auth                              # QR login + bootstrap sync (once)
whatsapp-cli auth --follow                     # keep syncing after auth
whatsapp-cli auth --idle-exit 30s              # exit after idle during bootstrap
whatsapp-cli auth status
whatsapp-cli auth logout
whatsapp-cli doctor                            # store / auth / search diagnostics
whatsapp-cli doctor --connect                  # also try a live connection (needs lock)
```

**One-shot pattern (most common for agents):**

```bash
whatsapp-cli sync --once --json --idle-exit 5s
```

```bash
whatsapp-cli sync --once --idle-exit 5s        # pull until idle, then exit
whatsapp-cli sync --follow                     # continuous live sync (default for sync)
whatsapp-cli sync --once --refresh-contacts --refresh-groups
whatsapp-cli sync --download-media             # also download media in background
```

`sync` never shows a QR; if unauthenticated it errors with “run `whatsapp-cli auth`”.
Only one process may hold the store lock (`~/.whatsapp-cli/LOCK`) — stop `sync`/`listen`
before other write operations if doctor reports a lock conflict.

## Command tree

```
whatsapp-cli auth          [flags] | status | logout
whatsapp-cli doctor        [--connect]
whatsapp-cli sync          [--once|--follow] [--idle-exit] [--download-media]
                    [--refresh-contacts] [--refresh-groups] [--max-reconnect]
whatsapp-cli listen        [--events] [--chat] [--from] [--exclude-self] [--raw]
whatsapp-cli chats         list | show
whatsapp-cli contacts      search | show | refresh | alias set|rm | tags add|rm
whatsapp-cli messages      list | search | show | context
whatsapp-cli send          text | file | chat-presence | receipt | presence
whatsapp-cli media         download   # IPC-forwarded when listen is running
whatsapp-cli history       backfill
whatsapp-cli groups        list | refresh | info | rename | leave | join |
                    participants add|remove|promote|demote | invite link get|revoke
whatsapp-cli version
```

## Chats & contacts

```bash
whatsapp-cli chats list --limit 50 --query "John"
whatsapp-cli --json chats list --limit 1000
whatsapp-cli chats show --jid <JID>

whatsapp-cli contacts search "Alice"
whatsapp-cli contacts show --jid <JID>
whatsapp-cli contacts refresh
whatsapp-cli contacts alias set --jid <JID> --alias "Boss"
whatsapp-cli contacts alias rm --jid <JID>
whatsapp-cli contacts tags add --jid <JID> --tag work
whatsapp-cli contacts tags rm --jid <JID> --tag work
```

## Messages (offline after sync)

```bash
whatsapp-cli messages list --chat <JID> --limit 50
whatsapp-cli messages list --after 2026-03-01 --before 2026-04-01
whatsapp-cli messages search "invoice" --limit 20
whatsapp-cli messages search "meeting" --chat <JID> --after 2026-03-01
whatsapp-cli messages search "photo" --type image
whatsapp-cli messages show --chat <JID> --id <MSG_ID>
whatsapp-cli messages context --chat <JID> --id <MSG_ID> --before 5 --after 5
whatsapp-cli --json messages search "keyword" | jq .
```

## Read a conversation

Read the earlier messages of a chat with `messages list`. WhatsApp has no threads. A conversation is a DM, or a reply chain in a group. `--before` and `--after` take a time.

```bash
whatsapp-cli --json messages list --chat <JID> --limit 50
whatsapp-cli --json messages list --chat <JID> --before <time> --limit 50
whatsapp-cli messages show --chat <JID> --id <MSG_ID>
```

In the JSON output, `ReplyToID` is the id of the quoted message. Use `messages show` with that id to read the quoted message. When the local store has no older messages, run `history backfill --chat <JID>` first, then read again.

## Send

**Always confirm recipient JID + full message text with the user before sending.**

```bash
whatsapp-cli send text --to "+15551234567" --message "Hello from *CLI*!"
whatsapp-cli send text --to "123456789012345@g.us" --message "Team update"

whatsapp-cli send file --to "+15551234567" --file ./report.pdf --caption "Q1 report"
whatsapp-cli send file --to <JID> --file /path/image.jpg --filename "photo.jpg"
whatsapp-cli send file --to <JID> --file ./clip.mp4 --mime video/mp4

# Typing / recording indicator (chat presence; no message is stored)
whatsapp-cli send chat-presence --to "+15551234567"                    # composing (default)
whatsapp-cli send chat-presence --to <JID> --state paused
whatsapp-cli send chat-presence --to <JID> --state composing --media audio

# Read receipt (blue double checks) / played (voice notes, view-once)
whatsapp-cli send receipt --chat "+15551234567" --id <MSG_ID>
whatsapp-cli send receipt --chat <JID> --id id1 --id id2 --type read
whatsapp-cli send receipt --chat "123@g.us" --id <MSG_ID> --sender "+1555..."  # groups need sender
whatsapp-cli send receipt --chat <JID> --id <MSG_ID> --type played

# Global online/offline (enables gray double-check delivery receipts while connected)
whatsapp-cli send presence --state available
whatsapp-cli send presence --state unavailable
```

`--state` (chat-presence): `composing` (default) | `paused`. `--media`: `text` (default) | `audio` (voice-note recording).

**Receipts vs presence:**
- **Gray ✓✓ (delivered):** automatic when the session is online (`send presence --state available` or `listen --presence available`) and a message is received. Not a separate `send receipt` type.
- **Blue ✓✓ (read):** `send receipt --type read` (default). Needs message `--id`(s); groups need `--sender` unless the message is in the local DB.
- **Played:** `send receipt --type played` for voice notes / view-once.
- Privacy: if the account has read receipts disabled, WhatsApp may only sync as `read-self` to your other devices.

Forwarded over the listen Unix socket when a daemon holds the connection (same as `send text`/`file`).

WhatsApp markdown in `--message` / `--caption`:

| Style | Syntax |
| --- | --- |
| Bold | `*text*` |
| Italic | `_text_` |
| Strikethrough | `~text~` |
| Monospace block | triple backticks |
| Inline code | `` `text` `` |
| Bulleted list | `- item` |
| Numbered list | `1. item` |
| Quote | `> text` |

If `whatsapp-cli listen` (or a live sync process that owns the connection) is running,
`send` is forwarded over a Unix socket in the store dir so you do not need to
stop the listener.

## Groups

```bash
whatsapp-cli groups list --query "Project"
whatsapp-cli groups refresh                          # live fetch joined groups → local DB
whatsapp-cli groups info --jid "123@g.us"
whatsapp-cli groups rename --jid "123@g.us" --name "New Name"
whatsapp-cli groups participants add --jid "123@g.us" --user "+1555..."
whatsapp-cli groups participants remove --jid "123@g.us" --user <JID>
whatsapp-cli groups participants promote --jid "123@g.us" --user <JID>
whatsapp-cli groups participants demote --jid "123@g.us" --user <JID>
whatsapp-cli groups invite link get --jid "123@g.us"
whatsapp-cli groups invite link revoke --jid "123@g.us"
whatsapp-cli groups join --code <INVITE_CODE>
whatsapp-cli groups leave --jid "123@g.us"
```

## Media & history backfill

```bash
whatsapp-cli media download --chat <JID> --id <MSG_ID>
whatsapp-cli media download --chat <JID> --id <MSG_ID> --output /tmp/out.jpg
# When listen holds the store lock, download is forwarded over its Unix socket
# (same as send). Live listen also persists messages so media metadata is available.

# Best-effort older history (primary phone must be online)
whatsapp-cli history backfill --chat <JID> --requests 5 --count 50
whatsapp-cli history backfill --chat <JID> --requests 3 --count 50 --idle-exit 5s
```

Backfill is **best-effort**; WhatsApp may not return full history. Recommended
`--count` is `50` per request. Anchors on the oldest local message in that chat.

## Listen (live JSONL)

```bash
whatsapp-cli listen                                          # messages,receipts,connection
whatsapp-cli listen --presence available                     # online + gray ✓✓ on receive
whatsapp-cli listen --events messages,groups --chat <JID>
whatsapp-cli listen --events all --exclude-self
whatsapp-cli listen --raw                                    # raw whatsmeow events
```

Stdout is JSONL: `{"t":"<event>","ts":"<rfc3339>","d":{...}}`. Status on stderr.
Exclusive store lock — cannot run alongside `sync` on the same store. A second
`listen` attaches to the first anchor and streams a filtered view.

On disconnect, reconnect uses exponential backoff (**2s → 4s → … → 30s**). The
budget is measured from the **first** disconnect of an outage until a successful
`connected` event; flapping does not reset the clock. Default `--max-reconnect`
is **5m** (then exit non-zero); `0` means unlimited. Immediate exit (no retries)
on `logged_out`, `temporary_ban`, or `client_outdated`.

Event categories: `messages`, `receipts`, `connection`, `calls`, `presence`,
`groups`, `appstate`, `newsletters`, `media`, `security`, `history`, `all`.
(`history` is opt-in; large initial-sync batches.)

## JID format

- Individual: `+15551234567@s.whatsapp.net` or `15551234567@s.whatsapp.net` (phone also accepted for `--to`)
- Group: `123456789012345@g.us` (or with hyphen form from older clients)

Discover JIDs first:

```bash
JID=$(whatsapp-cli --json chats list --query "Alice" | jq -r '.[0].JID')
```

## Agent workflows

**Daily sync + recent messages**

```bash
whatsapp-cli sync --once --idle-exit 5s --json && \
  whatsapp-cli messages list --limit 15
```

**Search then reply (after user confirms send)**

```bash
whatsapp-cli sync --once --idle-exit 5s
whatsapp-cli messages search "proposal" --chat <JID>
whatsapp-cli send text --to <JID> --message "Updated proposal attached"
```

**Backfill all known chats (slow)**

```bash
whatsapp-cli --json chats list --limit 100000 \
  | jq -r '.[].JID' \
  | while read -r jid; do
      whatsapp-cli history backfill --chat "$jid" --requests 3 --count 50
    done
```

## Best practices

1. Run `whatsapp-cli sync --once --idle-exit 5s` before read/send when you need freshness.
2. **Never send without explicit user confirmation** of recipient + message body.
3. Use `--json` + `jq` for reliable parsing.
4. Sessions persist for weeks; no re-QR unless `auth logout` or device revoked.
5. One writer per store: avoid concurrent `sync`/`listen`/mutating commands.
6. Store files: `session.db` (whatsmeow), `whatsapp-cli.db` (messages/FTS), `media/`, `LOCK`.

## Where things live

- Source: `connectors/whatsapp-cli/` in `github.com/trebi-ai/trebi-connectors`
- Build: `connectors/whatsapp-cli/scripts/build.sh` → `connectors/whatsapp-cli/build/whatsapp-cli`
- Binary name: `whatsapp-cli`. Trebi installs it into the connector folder from the catalog.
- Store: `$TREBI_STATE_DIR` when Trebi runs the CLI (then `--store` is an error), else `--store`, `$WHATSAPP_CLI_STORE_DIR`, or `~/.whatsapp-cli`
- Trebi channel: `whatsapp-cli serve` speaks `trebi-connector/1` on stdin and stdout. The Trebi daemon starts it. While it runs, the send commands forward to it. `serve --sandbox` runs the same adapter over a fake WhatsApp for tests.
- Module: `github.com/trebi-ai/trebi-connectors/connectors/whatsapp-cli` (cobra, not urfave/cli)

When in doubt: `whatsapp-cli <command> --help`.
