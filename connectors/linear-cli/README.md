# linear-cli

`linear-cli` is a small command line tool for Linear. It lists teams and issues. Its `serve` command speaks the `trebi-connector/1` protocol, so Trebi can start work when an issue or a comment changes.

## Use on its own

Build it with `scripts/build.sh [version] [output]`, or with `go build .`.

Give the tool a Linear personal API key. Make one in Linear under Settings, then Security & access. The tool reads the key in this order: the `--key` flag, the `LINEAR_API_KEY` env, the nearest `.env` file from the working folder up, then `~/.cli-tools/linear-cli/config.json` with the content `{"key": "lin_api_…"}`.

```bash
linear-cli teams list
linear-cli --json issues list --team ENG --limit 10
linear-cli --version
```

## Use with Trebi

Add the Linear connector from the catalog and set the "API key" input. Trebi runs `linear-cli serve` with `TREBI_STATE_DIR`. In this mode the tool reads the key only from `LINEAR_API_KEY`, and the `--key` flag fails with "Trebi sets this value".

Watch a team in the connection to get `issue` and `comment` events. With Trebi Cloud, the adapter makes one Linear webhook for each watch and checks the `linear-signature` header of each delivery. Only a Linear admin can make a webhook. Without Trebi Cloud, the adapter reads the changed issues and comments every two minutes.

The adapter keeps its state in `linear-state.json` in the state folder: the teams, the webhooks with their secrets, and the poll position. It writes the file with a temp file and a rename.

`linear-cli serve --sandbox` runs the adapter against a fake Linear in the same process. Outside Trebi it sends a fixed fake key to the fake, never a real key. The conformance check uses it:

```bash
scripts/validate.sh --conformance linear
```

The skill for agents is `catalog/linear/skill/SKILL.md`.
