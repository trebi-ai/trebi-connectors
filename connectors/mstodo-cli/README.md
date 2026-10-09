# mstodo-cli (`mstodo-cli`)

A small Go CLI for [Microsoft To Do](https://learn.microsoft.com/graph/api/resources/todo-overview) through Microsoft Graph. It reads lists and tasks. Its `serve` command is the Trebi connector: it watches lists with Graph change notifications, and with a delta query when there is no webhook.

## Build

```bash
# from this directory
go build -o mstodo-cli .

# or use the build script (writes ./build/mstodo-cli with version metadata)
MSTODO_CLIENT_ID=<app id> ./scripts/build.sh
```

Requires Go 1.25+. The login needs the id of a Microsoft Entra app that is a public client with the device code flow on. The build script puts `MSTODO_CLIENT_ID` into the program. A build without it cannot log in, but `serve --sandbox` still works.

## Use on its own

With no `TREBI_STATE_DIR` in the env, `mstodo-cli` is a normal CLI.

```bash
mstodo-cli auth login                 # show a code, then wait for the login
mstodo-cli lists list                 # the lists: id and name
mstodo-cli tasks list <list id>       # the tasks of one list
mstodo-cli --json lists list          # JSON output
```

`auth login` asks for `Tasks.ReadWrite` and `offline_access`. It writes the tokens to `~/.cli-tools/mstodo-cli/auth.json`. The CLI refreshes the token when it expires and saves the new token there. There is no env var for a token.

## Use with Trebi

Trebi starts `mstodo-cli serve` with `TREBI_STATE_DIR` set. This is Trebi mode:

- The login is only in `TREBI_STATE_DIR/auth.json`. The person logs in from the connection in Trebi with a device code. `serve` writes the file.
- `auth login` fails with "Set this value in Trebi".
- The CLI commands in a job read `auth.json` and keep a refreshed token in memory. Only `serve` writes the state folder.
- With no login, `serve` starts and sends `status` `auth_required`.
- `serve` keeps the subscriptions and the delta links in `mstodo-state.json` in the state folder.

A subscription watches one list:

- With a webhook (a Trebi Cloud link), `sync` creates a Graph subscription for each list, with a random `clientState` for each one. Graph checks the URL with `validationToken` before it answers, and the cloud echoes the token. The adapter renews each subscription when 75% of its time is gone. A lifecycle notification renews it (`reauthorizationRequired`), creates it again (`subscriptionRemoved`), or runs one delta query (`missed`).
- Without a webhook, the mode is `poll`. The adapter runs a delta query on each list every 5 minutes.

`serve --sandbox` runs the same adapter over a fake Graph in the same process, with the lists Groceries and Work. The device code login finishes by itself after 200 ms. The fake checks the notification URL as Graph does, then posts one change. Outside Trebi, the sandbox keeps its state in a temp folder.

The rules for all connector programs are in "Adapter folder contract" in `../../CLAUDE.md`.
