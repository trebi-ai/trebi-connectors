---
name: linear-cli
description: >
  Linear CLI tool. Use when the user wants to list Linear teams, read the issues that changed last, or start work when an issue or a comment changes in Linear.
  Triggers: "linear teams", "linear issues", "watch a linear team", "when a linear issue changes".
---

# linear-cli — Linear CLI

Go CLI for Linear: teams and issues. In Trebi it also sends `issue` and `comment` events. Binary: `linear-cli`.

## Auth

The key is a Linear personal API key. In Trebi, the connection input "API key" (`LINEAR_API_KEY`) sets it, and the `--key` flag fails with "Trebi sets this value".

On its own, the order is: the `--key` flag, the `LINEAR_API_KEY` env, the nearest `.env` from the working folder up, then `~/.cli-tools/linear-cli/config.json` (`{"key": "lin_api_…"}`).

## Commands

Add `--json` (or `-j`) before the command for JSON output.

| Command | What it does |
|---|---|
| `linear-cli teams list` | List the teams: key, name, and id. |
| `linear-cli issues list [--team ENG] [--since 2026-10-01T00:00:00Z] [--limit 25]` | List the issues that changed last, with state and time. |
| `linear-cli serve [--sandbox]` | Serve the `trebi-connector/1` protocol for Trebi. `--sandbox` uses a fake Linear in the same process. |
| `linear-cli --version` | Show the version. |

## Events

- `issue`: an issue was created, changed, or removed. The room is the team key, for example `ENG`. `data` has `action`, `id`, `identifier`, `title`, `url`, `state`, and `team`.
- `comment`: a comment on an issue. The room is the team key of the issue. `data` has `action`, `id`, `body`, `url`, and `issue`.

To get events, watch a team in the connection. An empty team watches every public team. "Changes to" selects Issue, Comment, Project, or Cycle; only Issue and Comment give events now.

With Trebi Cloud, Linear sends each change at once. Only a Linear admin can let Trebi watch a team this way. Without Trebi Cloud, Trebi checks for changes every two minutes.

A switch between the live mode and the check every two minutes can send one change two times with a different event id. A job that must act one time for each change can check the issue `id` and `updatedAt`.

## Examples

```bash
linear-cli teams list
linear-cli -j issues list --team ENG --limit 10
```
