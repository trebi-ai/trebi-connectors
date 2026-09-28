# discord-cli (`discord-cli`)

A small Go CLI that wraps the [Discord REST API](https://discord.com/developers/docs/reference) and the Discord Gateway WebSocket. Drives messages, reactions, threads, channels, and servers (guilds) for bots — and tails real-time gateway events as JSONL.

## Build

```bash
# from this directory
go build -o discord-cli .

# or use the build script (writes ./build/discord-cli with version metadata)
./scripts/build.sh
```

Requires Go 1.25+.

## Auth

The CLI resolves the bot token in this order:

1. `--token` flag
2. `DISCORD_TOKEN` env var, then `DISCORD_BOT_TOKEN` (a `.env` file in the current or any parent directory is auto-loaded)
3. `~/.cli-tools/discord-cli/config.json` (written by `auth set`)

```bash
# Save token to ~/.cli-tools/discord-cli/config.json (mode 0600)
./discord-cli auth set MTAxxx.YourBotTokenHere

# Print masked token + source
./discord-cli auth show

# Validate the token against /users/@me
./discord-cli auth test
```

## Examples

```bash
# Send a message
./discord-cli message send <channel_id> "hello world"

# Send with an embed and a file
./discord-cli message send <channel_id> "see attached" \
    --embed-title "Report" --embed-desc "weekly stats" \
    --file ./report.pdf

# List recent messages (paginates by snowflake; --before/--after accept ID, RFC3339, or YYYY-MM-DD)
./discord-cli message list <channel_id> --limit 50 --before 2026-05-01

# Reply
./discord-cli message reply <channel_id> <message_id> "thanks!"

# Search content + embeds (client-side scan)
./discord-cli message search -c '#general' -q 'invoice' --scan 500 --limit 20

# Reactions
./discord-cli reaction add <channel_id> <message_id> 👍
./discord-cli reaction users <channel_id> <message_id> 👍

# Threads
./discord-cli thread create <channel_id> <message_id> "follow-up"
./discord-cli thread list <guild_id>
./discord-cli thread send <thread_id> "in the thread"

# Channels
./discord-cli channel list                       # all guilds
./discord-cli channel list --server <guild_id>
./discord-cli channel create <guild_id> announcements --type text --topic "..."
./discord-cli channel typing <channel_id>        # typing indicator (~10s)

# Servers (guilds)
./discord-cli server list --counts
./discord-cli server info <guild_id> --counts

# Real-time gateway events (JSONL on stdout, status on stderr)
./discord-cli listen --events messages,reactions --server <guild_id>
./discord-cli listen --events messages,threads --server <guild_id> --channel <parent_channel_id>
./discord-cli listen --channel <channel_id> --include-bots
```

`listen --channel` includes **child threads** of that channel (messages in a thread use `channel_id` = thread id). On `MESSAGE_*` / reaction events in a thread, the CLI adds `d.parent_id` (parent text channel). Event categories: `messages`, `reactions`, `members`, `voice`, `threads`.

## Output

- Default output is human-readable text / tables.
- `--json` / `-j` emits JSON, suitable for `jq`. `listen` always emits JSONL.

## Subcommands

| Subcommand | What it covers |
|---|---|
| `auth` | Token management (`set`, `show`, `test`) |
| `message` | `send`, `list`, `get`, `edit`, `delete`, `reply`, `search`, `bulk-delete` |
| `reaction` | `add`, `remove`, `list`, `users` |
| `thread` | `create`, `list`, `send`, `archive`, `unarchive`, `rename`, `add-member`, `remove-member` |
| `channel` | `list`, `create`, `delete`, `info`, `edit`, `typing` |
| `server` (alias `guild`) | `list`, `info` |
| `listen` | Stream gateway events as JSONL |

Run `./discord-cli <command> --help` for full subcommand listings.

## Layout

```
main.go               # urfave/cli root, wires subcommands and resolves token
cmd/                  # one file per noun (auth, message, reaction, thread, channel, server, listen)
internal/client/      # Discord REST client (HTTP + multipart upload) and types
internal/gateway/     # Discord Gateway WebSocket client (heartbeat, IDENTIFY, dispatch loop)
internal/config/      # ~/.cli-tools/discord-cli/config.json + .env loader
scripts/build.sh      # build with version metadata into ./build/
```

## Tests

The integration tests in `discord-cli_test.go` make live calls to Discord. They skip unless these env vars are set (a `.env` in the repo root works):

| Variable | Purpose |
|---|---|
| `DISCORD_BOT_TOKEN` | Bot token used for all calls |
| `DISCORD_TEST_CHANNEL_ID` | Channel where test messages are sent and cleaned up |
| `DISCORD_TEST_GUILD_ID` | Guild used by channel + thread workflow tests |

```bash
go test ./...
```

Tests create and delete their own messages / channels / threads.
