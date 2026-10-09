# notion-cli (`notion-cli`)

A small Go CLI for the [Notion API](https://developers.notion.com/reference). It searches the pages that are shared with an internal integration. Its `serve` command is the Trebi adapter that turns page and database changes into events.

## Build

```bash
# from this directory
go build -o notion-cli .

# or use the build script (writes ./build/notion-cli with version metadata)
./scripts/build.sh
```

Requires Go 1.25+.

## Use on its own

With no `TREBI_STATE_DIR` in the env, `notion-cli` is a normal CLI. It finds the integration secret in this order:

1. The `--token` flag.
2. The env var `NOTION_TOKEN`.
3. The nearest `.env` file, from the current folder up. It reads `NOTION_TOKEN`. It does not change the env.
4. `~/.cli-tools/notion-cli/config.json`, which `auth set` writes.

`auth show` prints the masked secret and the place that it came from.

```bash
./notion-cli auth set secret_xxx         # save the secret (mode 0600)
./notion-cli auth test                   # GET /v1/users/me
./notion-cli pages search --query plan   # newest edit first
./notion-cli --json pages search
```

## Use with Trebi

Trebi starts `notion-cli serve` with `TREBI_STATE_DIR` set. This is Trebi mode. The Trebi values win over everything:

- The secret comes only from the `NOTION_TOKEN` input of the connection. The CLI reads no `.env` file and no config file.
- `--token` fails with "Trebi sets this value".
- `auth set` fails with "Set this value in Trebi". Change the secret in the connection settings.
- With no secret, `serve` starts and sends `status` `auth_required` with reason `missing_input`, so Trebi shows the setup form.

The adapter has one subscription, "Watch this workspace", with no fields. It sends `page` and `database` events.

- With a hosted webhook URL, the mode is `manual`. The person adds a webhook subscription with the URL in the Notion integration settings. Notion then posts a verification token. The adapter keeps it and shows it as the code to paste into Notion. The first delivery with a good `x-notion-signature` makes the subscription active. The adapter reads the page or the database before it sends the event.
- Without a hosted URL, the mode is `poll`. The adapter searches for pages edited since the cursor every 5 minutes.
- When the hook is verified, the adapter refuses a different verification token. A new hook URL, or the removal of the subscription, starts the setup again.
- `serve` keeps its state in `TREBI_STATE_DIR/notion-state.json`: the hook URL, the verification token, and the poll cursor.
- `serve --sandbox` runs the same adapter over a fake Notion in the same process. When a sync needs the person, the adapter gives the hook URL to the fake. The fake then posts the verification token and one signed page event to the URL. It needs no account. Outside Trebi it uses a fixed fake secret.

The rules for all connector programs are in "Adapter folder contract" in `../../CLAUDE.md`.
