---
name: connector-authoring
description: Build or change a Trebi connector program in trebi-connectors - the catalog manifest, the `serve` command with the Go SDK, the adapter folder contract, subscriptions and webhooks, Trebi mode and standalone mode, `serve --sandbox` over a fake service, the conformance check, `scripts/build.sh`, and the version bump. Use it before you add a catalog entry with a program, change `connectors/<cli>/`, or change the SDK.
---

# Build a connector program

A connector program is a normal CLI plus a `serve` command. `serve` speaks `trebi-connector/1` (JSON-RPC 2.0, one object per line) on stdin and stdout. The Trebi daemon starts it from `events.command` of the manifest. Examples: `connectors/discord-cli` (pure Go) and `connectors/whatsapp-cli` (CGO and SQLite).

Read the "Adapter folder contract" in `CLAUDE.md` at the repository root first. This skill tells you how to meet it.

## 1. The manifest

Make `catalog/<name>/trebi-connector.yaml`. The folder name is `name`. The schema is `schema/trebi-connector.schema.json`.

```yaml
schema: trebi-connector/1
name: example
title: Example
description: "One sentence for the user."
version: 0.1.0
publisher: trebi
source: github.com/trebi-ai/trebi-connectors
platforms: [darwin/arm64, darwin/amd64, linux/arm64, linux/amd64]
install:
  catalog: true            # the catalog builds connectors/<bin>
  bin: example-cli
  version_command: example-cli --version
setup:
  login: [qr]              # only for an interactive login
  inputs:
    - {name: EXAMPLE_TOKEN, label: API token, secret: true, required: true}
actions:
  cli: {commands: [example-cli]}
events:
  protocol: trebi-connector/1
  command: example-cli serve
  restart: always
  types:
    - {type: message, description: A new message in a room, schema: schemas/message.json}
channel:
  features: [rooms.list, history, replay]
  limits: {max_text: 4000, formats: [text]}
```

- Each input has one env name. The `label` is the name that the user sees. Use a verb for a user action.
- `channel.features` and `channel.limits` must match what `initialize` returns. The adapter must not promise more than the manifest.
- Put the skill for the agent in `catalog/<name>/skill/SKILL.md`. It is the only copy.

## 2. The config package: two modes in one place

Write the choice between the two modes in `internal/config` of the program. No other code reads the home folder, a `.env` file, or a settings env name.

- Trebi mode is on when `sdk.FromEnv()` returns `ok`, which means `TREBI_STATE_DIR` is set.
- In Trebi mode, read each setting only from its input env name and from `TREBI_STATE_DIR`. A flag that selects a store or a credential returns an error with "Trebi sets this value". A command that saves a setting returns an error with "Set this value in Trebi".
- Standalone, keep the defaults of the tool: flags, env names, `.env` files, and a config file in the home folder.
- Give one function that resolves each setting and tells its source, for example `config.Resolve(flag) (Settings, error)` in `discord-cli` or `config.StoreDir(flag)` in `whatsapp-cli`.

## 3. The `serve` command with the SDK

```go
t, _ := sdk.FromEnv()
a, err := serve.New(client, version, t.StateDir)
if err != nil {
	return err
}
return sdk.Serve(ctx, a)
```

- The adapter implements `sdk.Adapter` (`Initialize`) plus one interface for each feature (`Sender`, `RoomLister`, `Historian`, `Replayer`, and so on). See `sdk/README.md`.
- `Runner.Run` holds the connection and emits events and status. Nothing goes out before `initialized`: the SDK queues it.
- A required input that is not set gives `sdk.MissingInput{Name: "EXAMPLE_TOKEN", Label: "API token"}` from `Initialize`. Never look for the value in another place. The SDK sends `status` `auth_required` with reason `missing_input`, and the process stays alive.
- Return typed errors: `sdk.NotFound` for an unknown room or message, `sdk.Invalid` for bad params, `sdk.Transient` for a network error.
- Stdout is for the protocol only. Write logs to stderr. Write no log file.
- The SDK keeps the send dedupe file in `TREBI_STATE_DIR`. Do not write your own.
- An adapter that implements `sdk.FolderUser` gets both folders before the first request.

## 4. State, cache, and a clean stop

- Put durable data (login session, message store, known rooms, downloads that the user refers to) in `TREBI_STATE_DIR`. Put temp files and data that you can build again in `TREBI_CACHE_DIR`.
- Store paths relative to the state folder. Join them with the folder when you put them in an event. An event path is absolute.
- Write a state file with a temp file and a rename, so a stop never leaves half a file.
- Give the state a version. Upgrade an older version at start. A newer version gives `status` `error` with a clear message, and the program changes nothing.
- On `shutdown`, flush and close each file. For SQLite, run `PRAGMA wal_checkpoint(TRUNCATE)` and close the database.
- At start, delete the temp files that a crash left.
- Hold one lock in the state folder. A CLI command in a job forwards to `serve` through a socket in the state folder. Keep the full socket path under 104 bytes: use a short name in the state folder.

## 5. `serve --sandbox`

`<cli> serve --sandbox` runs the real adapter of the program over a fake service in the same process. The conformance check runs it with no account and no network.

- Make the fake a package that the program and the tests both use, for example `internal/fakediscord` (an `httptest` server for the REST API and the gateway) or `internal/wa/fakewa` (a fake client behind the client interface).
- Give the fake fixed rooms, so `rooms/list` has at least one room.
- The fake echoes each send as a `message` event with `sender.self: true`.
- A fake login completes by itself. It keeps its state in `TREBI_STATE_DIR`, so a restart stays logged in.
- The sandbox writes nothing outside the state and cache folders. Outside Trebi, use a temp folder or fixed fake credentials. Never send a real credential to the fake.
- `sdk.NewSandbox` is the reference adapter of the SDK. Do not use it for the sandbox of a program.
- The sandbox delivery rule: for a connector with `events.subscriptions`, the fake registers a webhook when the adapter asks for one. It then POSTs one signed delivery to the webhook URL, with the same headers and the same signature form as the real system. The conformance check gives the URL and the secret, sends the delivery back with `webhook/receive`, and expects one event. It also changes one byte of the body and expects `invalid`.
- With no webhook, the fake serves the poll path, so `sync` gives `polling`.

## 6. Subscriptions

A connector with `events.subscriptions` lets a user watch things: a repository, a team, a list, or a workspace. `CONTRIBUTING.md` ("Watch things") has the manifest block, the modes, and the handshake rules. `sdk/README.md` ("Watch things") has the interfaces.

### The form and the room field

- Keep the form short. No field gives one switch. One dynamic field gives one picker.
- A `dynamic` field gets its options from `Subscriber.Options`. Filter by `q`. Return at most `limit` options and a `next` page token. `Values` has the current form, so one field can depend on another.
- When a webhook is available, list only the things that the login can register a webhook on (for GitHub, the repositories with admin access).
- The `room: true` field is the room of each event. Use the same room id in a poll event and in a webhook event.

### Choose a mode for each subscription

`Sync` gets the full list each time. Make the platform match it, and return one state for each subscription, in order.

- With `InitializeParams.Webhook` and a login that can register webhooks: mode `api`. Register, update, or reuse the platform webhook, then return `active`.
- With a webhook and no API to register one: mode `manual`. Return `action_required` with an action.
- With no webhook: mode `poll` (or `stream`). Return `polling`.
- A platform error that the user can fix: state `error` with a short `message`, for example "Trebi needs admin access to octo/app". A login that is gone: send `status` `auth_required`.
- Make `Sync` idempotent. Find an existing platform webhook by its URL before you create one. Delete each platform webhook in state that no subscription uses, and each one whose URL differs from the current URL.

### The secret and the signature

- `Webhook.Secret` is a suggestion. Give it to the platform when the API accepts a secret. When the platform makes its own secret, keep that secret in state.
- Check the signature in `ReceiveWebhook` before you parse the body. Use `req.VerifyHMAC` for the form `<prefix><HMAC of the body>`. Check another form in the adapter, with `hmac.Equal`.
- A bad signature returns `sdk.Invalid`. The daemon drops it.
- A request with `Handshake: true` was answered by the cloud. Keep a secret from it when the platform sends one, and make no event.
- Use the delivery id of the platform as the event id, so a retry does not start a job two times.

### Renewal

Some platforms let a webhook expire (Microsoft Graph after about 3 days). `Runner.Run` renews each one when 75% of its time is gone. Set `expires_at` in the state. When a renewal fails, call `EmitterFrom(ctx).SubscriptionsChanged()` or the `Emitter` of `Run`, so the daemon syncs again.

### Thin payloads

Some platforms send only an id (Notion, Microsoft Graph). Read the object with the API before you send the event. A deleted object has no read: send the id and the change type only. When the platform tells that it missed changes, read the changes since the last cursor in state.

### State

- Keep the platform webhook ids, the platform secrets, and the person answers in `TREBI_STATE_DIR`, keyed by the subscription id.
- A restore or a new Trebi Cloud link gives a new URL. The next `Sync` deletes the stale platform webhooks of the old URL and makes new ones.

### The action for a person

An `action` tells a person what to do to finish a `manual` subscription. Write `text` in simple words for a person who does not know webhooks. Do not use the words webhook, endpoint, payload, or subscription. Put the address to copy in `show`, the settings page in `url`, and each value to paste in `ask`. Mark a secret with `secret: true`. When the platform later sends a value that the person must paste back, call `SubscriptionsChanged()` so the UI shows it at once.

### The poll fallback

Always list `poll` or `stream` in `modes`. A user without a Trebi Cloud link has no webhook URL. `Run` polls at the interval that the platform allows, at least one minute, and keeps its cursor in state. A switch between a webhook and a poll can repeat one change with another event id. Tell this in the skill of the connector.

## 7. Tests

- Adapter tests: run the adapter over the fake with `sdktest.Start` and `sdk.WithStateDir(t.TempDir())`.
- A Trebi mode test: set `TREBI_STATE_DIR` to a temp folder and `HOME` to a second temp folder that holds a config file and a `.env` file. The program must not use them. `--store` or `--token` must fail.
- A standalone test: unset each `TREBI_*` variable and set `HOME` to a temp folder. The program must use its standalone defaults in their order.
- A restore test: copy the state folder to a new path, start again, and check that the login and the dedupe stay.

## 8. Build

`connectors/<cli>/scripts/build.sh [version] [output]` builds the program for the host platform. The catalog workflow runs it on native runners and stamps the entry version. Keep it working with `-trimpath`. A CGO program needs a C compiler on each runner.

The root `go.work` builds each program against the SDK in the same commit. When you change the SDK, the release steps are in "The SDK" in `README.md`: tag `sdk/vX.Y.Z`, then bump the require in `connectors/*/go.mod` and the `replace` version in `go.work`.

## 9. Version bump

Bump `version` in `catalog/<name>/trebi-connector.yaml` in the same PR as each change to the entry, its skill, or its program. A published version is immutable. On `main`, the catalog workflow builds and publishes the program of each new version.

## Checklist before a PR

- Durable data is only in `TREBI_STATE_DIR`. Temp data is only in `TREBI_CACHE_DIR`.
- No log file. Stderr only.
- No absolute path in a state file.
- An empty state folder gives `auth_required` or `connecting`, not a crash.
- State from the previous version loads.
- `shutdown` closes every file.
- The socket path stays under 104 bytes.
- A CLI command in a job reaches the `serve` process.
- In Trebi mode, the program reads no file in the home folder and no `.env` file, and it uses one env name per input.
- A missing required input gives `sdk.MissingInput`, not a value from somewhere else.
- A command that saves a setting fails in Trebi mode.
- With no `TREBI_*` variable, the program works on its own with its standalone defaults.
- A flag that selects a store or a credential fails in Trebi mode.
- With `events.subscriptions`: `Sync` is idempotent and returns one state for each subscription, in order.
- With `events.subscriptions`: `modes` lists `poll` or `stream`, and `Sync` with no webhook gives `polling`.
- `ReceiveWebhook` checks the signature first and returns `sdk.Invalid` for a bad one.
- Each `action` text uses simple words.
- The fake of `serve --sandbox` registers a webhook and POSTs one signed delivery.
- Stale platform webhooks of an old URL are deleted on the next `Sync`.

Then run the checks, and do the conformance check last:

```bash
(cd connectors/<cli> && go vet ./... && go test ./...)
scripts/validate.sh
scripts/validate.sh --conformance <name>
```

`--conformance` needs a `trebi` on `PATH` that has the `connector conformance` command. It builds the program, puts it on `PATH`, and runs `trebi connector conformance --manifest catalog/<name>`. Every suite must pass.
