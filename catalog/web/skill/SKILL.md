---
name: web-cli
description: >
  Web feeds and pages. Use when the user wants to read an RSS or Atom feed or the text of a web page, or start work when a feed has a new entry or a part of a page changes.
  Triggers: "watch a blog", "new post in a feed", "rss", "atom feed", "when the page changes", "watch a price on a page".
---

# web-cli — web feeds and pages

Go CLI that reads RSS and Atom feeds and the text of web pages. In Trebi it also checks each address at an interval and sends events. Binary: `web-cli`. It needs no login.

## Settings

In Trebi, the connection has two inputs:

- "Web addresses" (`WEB_URLS`): one address on each line. A feed address (RSS 2.0, RSS 1.0, or Atom) gives `entry` events. Another address is a page and gives `state.changed` events. To watch only a part of a page, put a CSS selector after the address: `https://shop.example/lamp #price`.
- "Check every" (`WEB_EVERY`): the interval, for example `30m` or `2h`. The default is `30m`. The minimum is `10m`.

On its own, `web-cli serve` reads the same env names and keeps its state in `~/.cli-tools/web-cli/`.

## Commands

| Command | What it does |
|---|---|
| `web-cli read <url> [--selector <css>] [--json]` | Read one address once: the entries of a feed, or the text of a page. |
| `web-cli serve [--sandbox]` | Serve the `trebi-connector/1` protocol for Trebi. `--sandbox` uses a fake site in the same process. |
| `web-cli --version` | Show the version. |

Use `read` to test an address and a selector before you add them to the connection.

## Events

- `entry`: a new entry in a feed. The room is the feed address, with the feed title as its name. `text` is the title and the link. `sender` is the author, when the feed gives one. `data` has `feed`, `feed_title`, `entry_id`, `title`, `link`, `author`, `published`, and `summary` (plain text, at most 2000 bytes). The event id comes from the feed address and the entry id, so an entry gives one event only.
- `state.changed`: the text of a page or of the selected part changed. The room is the page address, with the page title as its name. `data` has `key` (`text`, or `text <selector>`), `old`, and `new`. With a selector that finds more than one element, the text has one line for each element.

The first check of an address sends no event: it records the entries and the text. Then each check sends the new entries, oldest first, and one `state.changed` when the text is different. Scripts and styles are not part of the text.

When no address can be read, the connection shows an error with the reason. It goes back to connected after the next good check.

## Examples

```bash
web-cli read https://blog.golang.org/feed.atom
web-cli read https://shop.example/lamp --selector '#price'
web-cli read --json https://news.example/rss.xml
```
