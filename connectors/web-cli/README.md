# web-cli

A small Go CLI that reads RSS and Atom feeds and the text of web pages. Its `serve` command is the Trebi adapter that checks each address at an interval. A feed gives one `entry` event for each new entry. A page gives `state.changed` when its text, or the text of a CSS selector, changes. The catalog entry `catalog/web` uses it. The skill is `catalog/web/skill/SKILL.md`.

## Build

```bash
go build -o web-cli .
./scripts/build.sh            # writes ./build/web-cli with version metadata
```

## Use on its own

`web-cli read <url> [--selector <css>] [--json]` reads one address once.

`web-cli serve` reads these env names:

| Env | What it is |
|---|---|
| `WEB_URLS` | The addresses, one on each line. A word after an address is a part of its CSS selector. |
| `WEB_EVERY` | The interval, for example `30m`. The default is `30m`; the minimum is `10m`. |

With no `TREBI_STATE_DIR`, the state goes to `~/.cli-tools/web-cli/`.

## Use with Trebi

Trebi runs `web-cli serve` with `TREBI_STATE_DIR` set. The values come only from the inputs of the connection.

- With no address, `serve` sends `status` `auth_required` with reason `missing_input`.
- The state folder holds `web-feeds.json` (the seen entry ids of each feed, format 1) and `trebi-sdk-state.json` (the page texts of `sdk.StateWatcher`). A newer format gives `status` `error` and changes nothing.
- When no address can be read in one check, the status is `error`. The next good check sets `connected`.
- `serve --sandbox` watches a fake site (`internal/fakeweb`) in the same process with a 1 s interval. The RSS feed gets a new entry and the price on the page changes at each check.

The rules for all connector programs are in "Adapter folder contract" in `../../CLAUDE.md`.
