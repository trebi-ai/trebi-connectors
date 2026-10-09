---
name: notion-cli
description: >
  Notion CLI tool and event source. Use when the user wants to search Notion pages, or to start a job
  when a page or a database changes in Notion.
  Triggers: "search notion", "notion pages", "watch notion", "when a notion page changes".
---

# notion-cli — Notion CLI

Go CLI for the Notion API. Binary: `notion-cli`. It reads only the pages and databases that the user shares with the integration.

## Auth

The order on its own: the `--token` flag, then `NOTION_TOKEN`, then the nearest `.env` from the current folder up, then `~/.cli-tools/notion-cli/config.json`.

In Trebi (`TREBI_STATE_DIR` is set), the secret comes only from the "Integration secret" input of the connection. `--token` and `auth set` fail in Trebi mode. Change the secret in the Trebi connection settings.

```bash
notion-cli auth set <secret>     # save the secret to ~/.cli-tools/notion-cli/config.json
notion-cli auth show             # show the masked secret and its source
notion-cli auth test             # GET /v1/users/me
```

## Pages

```bash
notion-cli pages search                  # newest edit first
notion-cli pages search --query "plan"   # pages with "plan" in the title
notion-cli --json pages search --limit 5
```

## Events

Switch on "Watch this workspace" in Trebi to get these events:

- `page`: a page changed. `data` has `page_id`, `title`, `url`, `change`, `database_id`, and `last_edited_time`.
- `database`: a database changed. `data` has `database_id`, `title`, `url`, and `change`.

The room of an event is the database of the page, or `workspace`. The event text is the title.

With a Trebi Cloud link, Notion sends the changes live. The user adds a webhook subscription in the Notion integration settings and pastes a code from Trebi into Notion. Without a link, Trebi checks Notion for edited pages every 5 minutes. A poll gives only `page` events, with `change` set to `edited`.

A switch between live and poll can send the same change again with another event id. A job that must run once for each change checks `page_id` and `last_edited_time`.
