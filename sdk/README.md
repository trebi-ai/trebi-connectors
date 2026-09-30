# Trebi connector SDK for Go

`github.com/trebi-ai/trebi-connectors/sdk` is the Go SDK for the `trebi-connector/1` protocol. The protocol is JSON-RPC 2.0, one JSON object per line, over the stdin and stdout of one long-lived adapter process. The Trebi daemon starts the process from the `events.command` of a connector manifest.

The module depends only on the Go standard library. Tags are `sdk/vX.Y.Z`. The v1 API has a compatibility promise.

## What the SDK does for you

- It reads and writes the frames. A line is at most 1 MiB. A longer input line ends `Serve` with `ErrLineTooLong`. A longer event or result is refused.
- It answers `ping` and `shutdown`. After `shutdown`, it cancels the context of the adapter and returns within 3 s.
- It holds every event and status until the daemon sends `initialized`. Then it sends the first `status` and starts `Runner.Run`.
- It drops a second `messages/send` with the same `key`. The keys stay in `$TREBI_STATE_DIR/trebi-sdk-sent.jsonl` (the newest 10000 keys), so a restart does not send a message twice.
- It checks `limits.max_text` before it calls `Send`.
- It maps errors to the seven codes of the protocol. Return a typed `*sdk.Error` to choose the code. A timeout or a network error becomes `transient`. Any other error becomes `permanent`.
- It builds the feature list from the interfaces the adapter implements. A request for a feature that is not in the list answers `unsupported`.
- It runs login flows: it assigns the flow id, answers `auth/begin` with the first step, sends the later steps, and sends `auth/done` and the status changes.
- It reports a missing input. See "Trebi mode".

## Trebi mode

The daemon gives each connection two folders. `TREBI_STATE_DIR` holds durable data. `TREBI_CACHE_DIR` holds data that the program can build again. `sdk.FromEnv()` reads them. `ok` is true when `TREBI_STATE_DIR` is set: the program is then in Trebi mode. The full rules are in "Adapter folder contract" in `../CLAUDE.md`.

- `Serve` reads the folders from the env. `sdk.WithStateDir(dir)` and `sdk.WithCacheDir(dir)` replace them, for example in tests.
- An adapter that implements `FolderUser` gets the folders in `UseFolders(t)` before the first request.
- When a required input is not set, return `sdk.MissingInput{Name: "DISCORD_TOKEN", Label: "Bot token"}` from `Initialize`. `Serve` answers `initialize`, sends `status` `auth_required` with reason `missing_input`, and does not start `Runner.Run`. The process stays alive and answers `ping`, `auth/status`, and `shutdown`. Each other request answers `auth_required`. The daemon then shows the setup form and does not restart the program in a loop.

## The adapter

```go
type Adapter interface {
	Initialize(ctx context.Context, in sdk.InitializeParams) (sdk.InitializeResult, error)
}
```

Add the optional interfaces for the features you have:

| Interface | Feature | Methods |
|---|---|---|
| `Sender` | (core) | `messages/send` |
| `RoomLister` (+ `RoomGetter`) | `rooms.list` | `rooms/list`, `rooms/get` |
| `RoomOpener` | `rooms.open` | `rooms/open` |
| `ThreadLister` | `threads` | `threads/list` |
| `ThreadCreator` | `threads.create` | `threads/create` |
| `Historian` | `history` | `messages/history` |
| `Replayer` | `replay` | `events/replay` |
| `Typer` | `typing` | `typing` |
| `Seer` | `seen` | `messages/seen` |
| `Reactor` | `reactions` | `reactions/add` |
| `Editor` | `edit` | `messages/edit` |
| `StatusReporter` | | `auth/status` and the first `status` |
| `Authenticator` | `setup.login` | `auth/begin`, `auth/submit`, `auth/cancel`, `auth/logout` |
| `Runner` | | receives the `Emitter` after `initialized` |

The features `attachments.in` and `attachments.out` have no method. Put them in `InitializeResult.Features`. When `Features` is not empty, the SDK keeps only the listed features that the adapter can serve, in the listed order. When it is empty, the SDK lists every method feature the adapter implements. Without `RoomGetter`, the SDK answers `rooms/get` from the pages of `ListRooms`.

## A minimal channel

```go
type echo struct{}

func (echo) Initialize(ctx context.Context, in sdk.InitializeParams) (sdk.InitializeResult, error) {
	return sdk.InitializeResult{
		Adapter: sdk.AdapterInfo{Name: "echo", Version: "0.1.0"},
		Events:  []sdk.EventDecl{{Type: "message"}},
		Limits:  sdk.Limits{MaxText: 4000, Formats: []string{sdk.FormatText}},
	}, nil
}

func (echo) Send(ctx context.Context, m sdk.SendParams) (sdk.SendResult, error) {
	return sdk.SendResult{MessageID: "m1"}, nil
}

func (echo) Run(ctx context.Context, e sdk.Emitter) error {
	// Connect to the platform, then call e.Event for each new message.
	<-ctx.Done()
	return nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := sdk.Serve(ctx, echo{}); err != nil {
		log.Fatal(err)
	}
}
```

Stdout belongs to the protocol. Write logs to stderr only. The daemon puts stderr in the stream log of the connector.

## The reference sandbox

`sdk.NewSandbox(cfg)` is the reference adapter that `trebi connector conformance` must pass. It has three rooms and one thread. It serves every write feature on its rooms. It emits a `message` event with `sender.self: true` for each send. It keeps its login, rooms, and events in `$TREBI_STATE_DIR/sandbox.json` with relative paths only, so a restart or a restore stays logged in. An unknown room gives `not_found`. A send with no text and no file gives `invalid`. It writes nothing outside the state and cache folders.

Use it in SDK tests and as an example. A connector program does not use it for `serve --sandbox`: the sandbox of a program runs the real adapter of the program over a fake service, so the conformance check tests the program code. See `connectors/discord-cli/internal/fakediscord` and `connectors/whatsapp-cli/internal/wa/fakewa`.

## Tests

`sdktest` runs an adapter in-process from the daemon side:

- `sdktest.Run(t, adapter, "contract/flow.reply.jsonl")` plays a transcript. It sends each daemon line and checks that each adapter line matches, with `"<any>"` as a wildcard. It maps the request ids.
- `sdktest.Start(adapter)` returns a `Conn` with `Initialize`, `Call`, `Notify`, and `WaitNote` for single requests.

## Versions

The next tag is `sdk/v0.2.0`. It adds `Trebi`, `FromEnv`, `FolderUser`, `MissingInput`, `ReasonMissingInput`, and `WithCacheDir`, and the new behavior of `NewSandbox`. The changes are additive. After the tag, do step 3 of "The SDK" in `../README.md`.

## Contract fixtures

`contract/` holds the wire fixtures of the protocol, byte-identical to `trebi/internal/connectors/protocol/testdata/contract/`. Do not edit them here. Edit them in `trebi` first, then copy the folder. `TestVendoredContract` fails when a sibling `trebi` checkout has other bytes.
