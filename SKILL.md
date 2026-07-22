---
name: discord-cli
description: >
  Discord CLI tool for bots. Use when the user wants to send/read/search Discord messages,
  manage channels/threads/reactions, listen to real-time gateway events, or automate Discord bot actions.
  Triggers: "send discord message", "list discord channels", "search discord", "listen to discord",
  "discord bot", "react on discord", "create discord thread".
---

# discord-cli — Discord CLI

Go CLI for Discord bot operations: messages, channels, threads, reactions, and real-time gateway events.
Binary: `discord-cli`.

## Auth

Token resolution order: `--token` flag → `DISCORD_BOT_TOKEN` env → `~/.discord-cli/config.json`.
Auto-loads `.env` from CWD (walks up parents).

```bash
discord-cli auth set <token>       # save token to ~/.discord-cli/config.json
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

Typing uses `POST /channels/{id}/typing` (no body). Indicator expires after ~10s; there is no stop endpoint. Thread IDs work as channel IDs. Use before a slow reply:

```bash
discord-cli channel typing "$CHANNEL"
# ... compute answer ...
discord-cli message send "$CHANNEL" "here's the answer"
```

## Listen (Gateway)

Real-time event stream via WebSocket. Outputs JSONL to stdout.

```bash
discord-cli listen                                                          # all events
discord-cli listen --events messages,reactions --server <guild_id> --channel <channel_id>
discord-cli listen --include-bots
```

Event categories: `messages`, `reactions`, `members`, `voice` (default: all).

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
    command: ["discord-cli", "listen", "--events", "messages"]
    format: json
    restart: always
```

```markdown
---
working_dir: /path/to/project
on:
  - source: discord
    match: "event.d.author && !event.d.author.bot"
    extract: "({ channel: event.d.channel_id, msg: event.d.content, author: event.d.author.username })"
---
{{author}} said in {{channel}}: {{msg}}
```
