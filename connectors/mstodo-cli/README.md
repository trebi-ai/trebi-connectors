# mstodo-cli (`mstodo-cli`)

A small Go CLI for [Microsoft To Do](https://learn.microsoft.com/graph/api/resources/todo-overview) through Microsoft Graph. It covers each To Do surface of Graph v1.0: lists, tasks, steps (checklist items), links (linked resources), files (attachments), open extensions, delta queries, and change notification subscriptions. Its `serve` command is the Trebi connector: it watches lists with Graph change notifications, and with a delta query when there is no webhook.

## Build

```bash
# from this directory
go build -o mstodo-cli .

# or use the build script (writes ./build/mstodo-cli with version metadata)
MSTODO_CLIENT_ID=<app id> ./scripts/build.sh
```

Requires Go 1.25+. The login needs the id of a Microsoft Entra app that is a public client ("Allow public client flows" is on) with the delegated permission `Tasks.ReadWrite`. The build script puts `MSTODO_CLIENT_ID` into the program. A build without it can log in only with `--client-id` or the env `MSTODO_CLIENT_ID`. `serve --sandbox` works without an app.

The tenant of the login is `common` by default. An app that accepts only personal Microsoft accounts needs `consumers`: set `--tenant consumers` or `MSTODO_TENANT=consumers`. With `common`, such an app fails with `AADSTS9002346`.

## Use on its own

With no `TREBI_STATE_DIR` in the env, `mstodo-cli` is a normal CLI.

```bash
mstodo-cli auth login                                   # show a code, then wait for the login
mstodo-cli auth status
mstodo-cli lists list
mstodo-cli tasks list --list Groceries --status open
mstodo-cli tasks create --list Work --title "Send the report" --due 2026-10-20
mstodo-cli tasks complete --list Work --task <task id>
mstodo-cli checklist create --list Work --task <task id> --name "Draft"
mstodo-cli links create --list Work --task <task id> --app GitHub --name "Issue 42" --url https://github.com/o/r/issues/42
mstodo-cli attachments add --list Work --task <task id> --file ./report.pdf
mstodo-cli extensions create --list Work --task <task id> --name com.example.sync --data '{"id":"A-1"}'
mstodo-cli subscriptions create --list Work --url https://example.com/hook
mstodo-cli watch --list Work --interval 30s            # one JSON line for each change
mstodo-cli lists list --json                            # JSON output
```

Each command takes named flags, so the flags work in any order. `--list` takes a list id or a list name. `mstodo-cli <command> --help` shows all flags. `../../catalog/mstodo/skill/SKILL.md` shows each command.

`auth login` asks for `openid profile offline_access Tasks.ReadWrite`. The account name comes from the `id_token`, because a token with only `Tasks.ReadWrite` cannot read `/me`. It writes the tokens, the app id, and the tenant to `~/.cli-tools/mstodo-cli/auth.json`. A refresh uses the app and the tenant of the login. The CLI refreshes the token when it expires and saves the new token there. There is no env var for a token.

## Use with Trebi

Trebi starts `mstodo-cli serve` with `TREBI_STATE_DIR` set. This is Trebi mode:

- The app id and the tenant come from the connection inputs `MSTODO_CLIENT_ID` and `MSTODO_TENANT`. Without `MSTODO_CLIENT_ID`, the app of the build logs in. `--client-id` and `--tenant` fail with "Trebi sets this value".
- The login is only in `TREBI_STATE_DIR/auth.json`. The person logs in from the connection in Trebi with a device code. `serve` writes the file.
- `auth login` and `auth logout` fail with "Set this value in Trebi".
- The CLI commands in a job read `auth.json` and keep a refreshed token in memory. Only `serve` writes the state folder.
- With no login, `serve` starts and sends `status` `auth_required`.
- `serve` keeps the subscriptions and the delta links in `mstodo-state.json` in the state folder.

A subscription watches one list:

- With a webhook (a Trebi Cloud link), `sync` creates a Graph subscription for each list, with a random `clientState` for each one. Graph checks the URL with `validationToken` before it answers, and the cloud echoes the token. The adapter renews each subscription when 75% of its time is gone. A lifecycle notification renews it (`reauthorizationRequired`), creates it again (`subscriptionRemoved`), or runs one delta query (`missed`).
- Without a webhook, the mode is `poll`. The adapter runs a delta query on each list every 5 minutes.

`serve --sandbox` runs the same adapter over a fake Graph in the same process, with the lists Groceries and Work. The fake has each endpoint that the CLI uses, so the tests run each command against it. The device code login finishes by itself after 200 ms. The fake checks the notification URL as Graph does, then posts one change. Outside Trebi, the sandbox keeps its state in a temp folder.

The rules for all connector programs are in "Adapter folder contract" in `../../CLAUDE.md`.
