---
name: discord-cli
description: >
  Discord CLI tool for bots. Use when the user wants to send/read/search Discord messages,
  manage channels/threads/reactions, show a typing indicator, listen to real-time gateway
  events, or automate Discord bot actions.
  Triggers: "send discord message", "list discord channels", "search discord", "listen to discord",
  "discord bot", "react on discord", "create discord thread", "discord typing", "bot is typing",
  "channel typing".
---

# discord-cli — Discord CLI

Go CLI for Discord bot operations: messages, channels, threads, reactions, typing indicators, and real-time gateway events.
Binary: `discord-cli`.

## Auth

Token resolution order: `--token` flag → `DISCORD_BOT_TOKEN` env → `~/.cli-tools/discord-cli/config.json`.
Auto-loads `.env` from CWD (walks up parents).

```bash
discord-cli auth set <token>       # save token to ~/.cli-tools/discord-cli/config.json
discord-cli auth show              # show masked token + source
discord-cli auth test              # GET /users/@me
```

## Messages

```bash
discord-cli message send <channel_id> "text"
discord-cli message send <channel_id> "text" --embed-title "T" --embed-desc "D" --embed-color 0xff0000
discord-cli message send <channel_id> "text" --file ./a.png --file ./b.pdf
discord-cli message list <channel_id> --limit 50 --before 2025-06-01 --after 1234567890123456
discord-cli message get <channel_id> <msg_id>
discord-cli message edit <channel_id> <msg_id> "new text"
discord-cli message delete <channel_id> <msg_id>
discord-cli message reply <channel_id> <msg_id> "text" --file ./img.png
discord-cli message search -c <channel> -q "query" --limit 1 --scan 200 --author "user" --before 2025-06-01
discord-cli message bulk-delete <channel_id> <id1> <id2> ...
```

`--before`/`--after` accept: message ID (digits), date (`2025-01-15`), or RFC3339 (`2025-01-15T00:00:00Z`).
Search uses flags only: `-c`/`--channel` (ID or `#name`), `-q`/`--query`, `--scan` (messages to fetch, default 100, paginates), `-n`/`--limit` (max results, 0=all).
Matches content + embed text (title, description, footer, author, fields).

## Reactions

```bash
discord-cli reaction add <channel_id> <msg_id> 👍
discord-cli reaction add <channel_id> <msg_id> fire:123456   # custom emoji
discord-cli reaction remove <channel_id> <msg_id> 👍
discord-cli reaction list <channel_id> <msg_id>
discord-cli reaction users <channel_id> <msg_id> 👍 --limit 50
```

## Threads

```bash
discord-cli thread create <channel_id> <msg_id> "thread name" --auto-archive 60
discord-cli thread list <guild_id>                    # active threads
discord-cli thread list <channel_id> --archived       # archived threads
discord-cli thread send <thread_id> "text" --file ./f.txt
discord-cli thread archive <thread_id>
discord-cli thread unarchive <thread_id>
discord-cli thread rename <thread_id> "new name"
discord-cli thread add-member <thread_id> <user_id>
discord-cli thread remove-member <thread_id> <user_id>
```

## Channels

```bash
discord-cli channel list                              # all guilds
discord-cli channel list --server <guild_id>          # one guild
discord-cli channel info <channel_id>
discord-cli channel create <guild_id> "name" --type text --topic "desc"   # text|voice|category|forum
discord-cli channel edit <channel_id> --name "new" --topic "t" --slowmode 5 --nsfw
discord-cli channel delete <channel_id>
discord-cli channel typing <channel_id>               # "bot is typing…" (~10s; re-call to extend)
```

Typing uses `POST /channels/{id}/typing` (no body). Indicator expires after ~10s; there is no stop endpoint. Thread IDs and DM channel IDs work. Use before a slow reply (re-fire every ~8–10s on long work):

```bash
discord-cli channel typing "$CHANNEL"
# ... compute answer ...
discord-cli message send "$CHANNEL" "here's the answer"
```

`--json` prints `{"channel_id":"...","ok":true}`.

## Listen (Gateway)

Real-time event stream via WebSocket. Outputs JSONL to stdout.

```bash
discord-cli listen                                                          # all events
discord-cli listen --events messages,reactions --server <guild_id> --channel <channel_id>
discord-cli listen --events messages,threads --server <guild_id> --channel <parent_channel_id>
discord-cli listen --include-bots
```

Event categories: `messages`, `reactions`, `members`, `voice`, `threads` (default: all).

| Category   | Discord `t` values |
|------------|--------------------|
| `messages` | `MESSAGE_CREATE`, `MESSAGE_UPDATE`, `MESSAGE_DELETE` |
| `reactions`| `MESSAGE_REACTION_ADD`, `MESSAGE_REACTION_REMOVE` |
| `members`  | `GUILD_MEMBER_ADD`, `GUILD_MEMBER_REMOVE` |
| `voice`    | `VOICE_STATE_UPDATE` |
| `threads`  | `THREAD_CREATE`, `THREAD_UPDATE`, `THREAD_DELETE`, `THREAD_LIST_SYNC`, `THREAD_MEMBER_UPDATE` |

The `messages` category requests **guild + DM** intents (`GUILD_MESSAGES | DIRECT_MESSAGES | MESSAGE_CONTENT`), so DMs to the bot appear as `MESSAGE_CREATE` with empty/`null` `guild_id`.

`threads` needs only the **GUILDS** intent (always enabled). Use it for thread lifecycle; message content in threads still comes under `messages` (with `channel_id` = thread id).

Each line is `{"t":"<EVENT_TYPE>","d":{...}}` (Discord dispatch shape). Bot messages are filtered out unless `--include-bots` (thread lifecycle events have no author and are never bot-filtered).

**Thread messages + `parent_id` enrichment:**
Discord sets `d.channel_id` to the **thread id** for messages in threads (not the parent). The CLI injects `d.parent_id` on `MESSAGE_*` and `MESSAGE_REACTION_*` when it knows the channel is a thread (from gateway `THREAD_*` / thread `CHANNEL_*` cache, or one REST `GET /channels/{id}`). Parent-channel messages have no `parent_id`.

```json
// message in a thread under #marketing-sling
{"t":"MESSAGE_CREATE","d":{"id":"...","channel_id":"<thread_id>","parent_id":"1502847711018356766","guild_id":"...","content":"...","author":{...}}}

// message in the parent text channel (no parent_id)
{"t":"MESSAGE_CREATE","d":{"id":"...","channel_id":"1502847711018356766","guild_id":"...","content":"...","author":{...}}}
```

Jobi check: `!!event.d.parent_id` ⇒ message/reaction is in a thread; reply with `event.d.channel_id` (thread id). Parent text channel is `event.d.parent_id`.

**Filters (client-side after receive):**
- `--server` — guild_id must match (drops DMs).
- `--channel` — that channel **and its child threads**. Message/reaction events whose `channel_id` is a thread under the parent pass; `THREAD_*` events match on thread `id` or `parent_id`. Parent map is built from gateway `THREAD_*` / thread-typed `CHANNEL_*` events; unknown thread ids may be resolved once via REST `GET /channels/{id}`.

Jobi / marketing-channel example (parent + threads under `#marketing-sling`):

```bash
discord-cli listen --events messages,threads \
  --server 1053623890653499413 \
  --channel 1502847711018356766
```

## Global Flags

```bash
discord-cli --token <token> ...       # override token
discord-cli --json ...                # JSON output (alias: -j)
```

## As a jobi Source

```yaml
# ~/.jobi/sources.yaml
sources:
  - name: discord
    command: ["discord-cli", "listen", "--events", "messages,threads"]
    format: json
    restart: always
  # Optional: scope to one channel + its threads
  # command: ["discord-cli", "listen", "--events", "messages,threads",
  #           "--server", "<guild_id>", "--channel", "<parent_channel_id>"]
```

```markdown
---
working_dir: /path/to/project
on:
  - source: discord
    match: "event.t === 'MESSAGE_CREATE' && event.d.author && !event.d.author.bot"
    extract: "({ channel_id: event.d.channel_id, parent_id: event.d.parent_id || null, msg: event.d.content, author: event.d.author.username })"
    # Best-effort "bot is typing…" before the agent wakes (runs after match+extract).
    acknowledge: 'discord-cli channel typing "{{channel_id}}"'
---
{{author}} said in {{channel_id}}: {{msg}}
```

DM-only match (empty `guild_id`) plus a trusted author id:

```js
event.t === "MESSAGE_CREATE"
  && !!event.d && !event.d.guild_id
  && event.d.author && !event.d.author.bot
  && event.d.author.id === "1051677407913967657"
```
