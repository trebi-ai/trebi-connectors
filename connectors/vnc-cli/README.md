# vnc-cli

A Go CLI that controls a remote computer over VNC (RFB). It captures the screen, types, clicks, and watches the screen for changes. It is a Go port of [vncdotool](https://github.com/sibson/vncdotool) (MIT, see `NOTICE`). Its `serve` command is the Trebi adapter. The catalog entry `catalog/vnc` uses it.

The package `vnc` is a public RFB client. Perception imports it as `github.com/trebi-ai/trebi-connectors/connectors/vnc-cli/vnc`. The package `vnc/vnctest` is a fake RFB server for tests.

## Build

```bash
go build -o vnc-cli .
./scripts/build.sh            # writes ./build/vnc-cli with version metadata
```

## Use on its own

```bash
vnc-cli -s 192.168.1.20:1 capture screen.png         # display 1, port 5901
VNC_PASSWORD=... vnc-cli -s host::5900 info
vnc-cli -s host key ctrl-alt-del
vnc-cli -s host type --env DB_PASSWORD --enter      # the secret is not in the command line
vnc-cli -s host run -- move 100 200 click 1 type hello key enter
vnc-cli -s host --timeout 30s expect login.png       # wait until the screen matches a picture
vnc-cli -s host stable 1.5 -r 0,0,800,600            # wait until an area stops changing
vnc-cli -s host run script.vdo                       # play a vncdotool script
```

### Server address

| Form | Meaning |
|---|---|
| `HOST`, `HOST:N` | Display N (port 5900+N) when N is less than 100, else port N. |
| `HOST::PORT` | Port PORT. |
| `[v6]`, `[v6]:N`, `[v6]::PORT` | An IPv6 address. |
| `/path`, `./path`, `unix:/path` | A Unix socket. |
| `ws://...`, `wss://...` | A WebSocket (noVNC style servers, Proxmox). The path and the query stay. |

### Commands

| Command | What it does |
|---|---|
| `capture [-r X,Y,W,H] [--cursor none\|local\|server] FILE` | Save the screen as png, jpg, gif, or bmp. `-` writes PNG to stdout. `none` (the default) leaves the pointer out, `local` draws the pointer shape from the server, and `server` connects without the background session so that the server draws the pointer. |
| `info` | Show the name, the size, the pixel format, and the protocol. |
| `resize WIDTH HEIGHT` | Ask the server for another screen size (ExtendedDesktopSize). A refusal or a server without the extension gives exit code 30. |
| `key KEY...`, `key down KEY`, `key up KEY` | Press keys or combos (`enter`, `ctrl-c`, `ctrl-alt-del`, `f5`, `0xff0d`), or hold and release one key. Use `Down` for the arrow key. |
| `type TEXT\|-`, `type --env NAME`, `type --file PATH` | Type text. `--enter` presses enter after it. `--human` types with random pauses. `--force-caps` holds shift for upper case letters and shifted symbols. |
| `move X Y`, `click [BUTTON]`, `mouse down\|up BUTTON` | Move the pointer, click (1 left, 2 middle, 3 right), or hold and release a button. |
| `drag X Y [--step N] [--button B]` | Hold a button (default 1), move in steps, and release it. |
| `scroll DX DY` | Turn the wheel. DY > 0 scrolls down. DX > 0 scrolls right. |
| `paste TEXT\|-\|--file PATH`, `clipboard get` | Set or read the clipboard of the remote computer. |
| `expect FILE [-r X,Y]` | Wait until the screen (or the area at X,Y) matches the picture. |
| `stable SECONDS [-r X,Y,W,H]` | Wait until the screen (or the area) shows no change for SECONDS. |
| `run FILE\|-`, `run -- COMMAND...` | Play a vncdotool `.vdo` script, or a chain of vncdotool commands, on one connection. |
| `watch [-a [NAME=]X,Y,W,H]... [--once] [-d D]` | Watch areas of the screen and write a JSON line for each change. See "Watch". |
| `config set NAME --address A [--login U] [--secret-env NAME] [--default]`, `config rm NAME`, `config list` | Keep named servers in `~/.cli-tools/vnc-cli/config.toml`. |

`expect` and `stable` use the vncdotool compare (`vnc/compare.go`). `--fuzz N` (0 to 255) is the largest color difference of one pixel; the default comes from the pixel format (0 for 8 bits per channel, 8 for rgb565). `--blur R` blurs both pictures first; the default is 2 with `--jpeg-quality`, else 0.

### Settings

| Flag | Env | What it is |
|---|---|---|
| `-s`, `--server` | `VNC_SERVER` | The server address. |
| `-t`, `--target` | | A named server from the config file. The `default` target is used when no server is given. |
| `--user` | `VNC_USER` | The user name for an account login (Apple Remote Desktop, VeNCrypt Plain). |
| `-p`, `--password-env NAME` | `VNC_PASSWORD` | The env name that holds the password. There is no `--password` flag, so the password is never in the command line. When the server asks for a password that is not set and stdin is a terminal, vnc-cli asks for it. |
| `--tls-ca-cert FILE`, `--tls-insecure-skip-verify` | | The server certificate check for VeNCrypt and `wss://`. |
| `--timeout D` | | Stop with exit code 40 after D. The default is no limit. |
| `--delay N\|MIN-MAX` | | Milliseconds between commands and between typed keys. |
| `--encodings LIST`, `--jpeg-quality N`, `--pixel-format NAME` | | The encodings in order, lossy JPEG at quality 0 to 9, and a forced pixel format (`bgrx8888`, `rgbx8888`, `rgb565`). |
| `--session auto\|off` | | `auto` (the default) uses the background session. `off` connects for this command only, as vncdotool does. |
| `--idle-timeout D` | `VNC_IDLE_TIMEOUT` | The background session stops after this time with no command (default 5m). `0` keeps it until `session stop`. |
| `-j`, `--json` | | Write JSON. |

### Background session

The first command starts a background session that keeps the connection open. The next commands use it, so they do not connect and do not wait for a full screen: a capture takes some milliseconds. The session connects again when the connection drops, and a command waits for it up to `--timeout`. A failed login stops the session, so that a wrong password is not sent again.

- `session start`, `session status`, `session stop [--all]`, and `session list` control the sessions.
- The server, the user, and the connection flags (`--encodings`, `--jpeg-quality`, `--pixel-format`, and the TLS flags) select the session. Other values of these flags start another session.
- The session gets the password in its env, never in its command line. Its socket and log are in `~/.cli-tools/vnc-cli/run/` (folder mode 0700, socket mode 0600).
- The session asks the server for a shared connection, so other viewers stay connected.

The config file:

```toml
default = "lab"

[targets.lab]
server = "10.0.0.5:1"
user = "admin"
password_env = "LAB_VNC_PASSWORD"
```

### Watch

`watch` writes one JSON object per line to stdout. The events and their fields are those of `glkvm-cli watch`, so a script for one tool reads the output of the other.

| Event | When |
|---|---|
| `ready` | The watch starts. It gives the areas, the reference size (`ref`), and the screen (`picture`). |
| `change` | An area starts to change. It gives `changed_pct`, `changed_px`, and `bbox` of the changed pixels. |
| `stable` | The area has not changed for `--settle`. It gives `changing_ms` and the `bbox` against the state before the change. This state becomes the new base. |
| `clipboard` | The clipboard of the remote computer changed. It gives `text`. |
| `bell` | The remote computer rang the bell. |
| `resize` | The screen size changed. It gives `size`. A new `ready` follows, and the areas scale with the screen. |
| `cursor` | The pointer shape changed. It gives `cursor` with a hash and the hot spot. Add it with `--events`. |
| `disconnected`, `connected` | The connection dropped, and the connection is back. A new `ready` follows `connected`. |
| `error` | The watch failed. |

- Area coordinates are pixels of a reference image of size `--ref` (default: the screen). Without `--area`, the whole screen is one area named `screen`.
- A pixel changes when its brightness changes by more than `--pixel-delta` (default 24). An area changes when more than `--threshold` percent (default 0.5) and at least `--min-pixels` (default 16) of its pixels change.
- The server sends only the changed parts of the screen, so watch reads only the areas that they touch. `--max-fps` (default 10) limits the checks.
- `--events LIST` selects the events. `--save DIR` saves a PNG of the area with each `change` and `stable` event.
- `--once` stops after the first `stable` event. With `-d`, it stops with exit code 1 when no `stable` event comes in time. `-d` alone stops after the time with exit code 0.
- The watch uses the background session, so the session connects again when the connection drops. With `--session off`, the watch keeps its own connection and connects again too.

### Exit codes

The codes follow vncdotool, so that scripts can move over.

| Code | Meaning |
|---|---|
| 0 | All commands completed. |
| 1 | An unexpected error. |
| 2 | A bad command line. |
| 3 | The login failed, usually a wrong password. |
| 10 | vnc-cli cannot connect to the server. |
| 11 | The server closed the connection. |
| 20 | A protocol error, or a TLS certificate that is not trusted. |
| 30 | A command failed, for example an area that is not on the screen. |
| 40 | The time limit (`--timeout`) ended. |

## Use with Trebi

The catalog entry `catalog/vnc` ("Remote screen") runs `vnc-cli serve`. The skill of the entry, `catalog/vnc/skill/SKILL.md`, is the full guide for agents.

When `TREBI_STATE_DIR` is set, the server, the user, and the password come only from `VNC_SERVER`, `VNC_USER`, and `VNC_PASSWORD`. `--server`, `--target`, `--user`, and `--password-env` fail with "Trebi sets this value". `config set` and `config rm` fail with "Set this value in Trebi".

- `serve` keeps the connection, and it is the background session of the Trebi connection. It listens on `$TREBI_STATE_DIR/vnc.sock` and holds `serve.lock`. A command in a job sends its work to that socket and never connects by itself. When `serve` does not run, a command fails with exit code 10.
- `serve` runs the watch engine on the areas of `VNC_AREAS` (`NAME=X,Y,W,H` separated by `;`), with the quiet time of `VNC_SETTLE`. It sends `screen.changed`, `screen.stable`, `clipboard.changed`, `bell`, and `screen.resized`, and `state.changed` and `state.recovered` for the key `reachable`.
- With `VNC_IMAGES` set to `yes` (the default), each `screen.stable` event has a PNG of the area. The pictures are in `$TREBI_STATE_DIR/images/`, and the newest 200 stay.
- The status is `connecting`, then `connected`. A failed login gives `auth_required` and stops the connection, so that a wrong password is not sent again. A computer that cannot be reached gives `error`, and `serve` connects again with a wait of 1s to 30s.
- `serve --sandbox` runs the same adapter over a fake screen in the same process. The fake changes the screen each second and the clipboard each 10s. It replaces the server and the password, so no real password goes to it.

## Tests

```bash
go test -race ./...
docker compose -f fleet/docker-compose.yml up -d --build --wait
go test -p 1 -tags fleet ./...                       # TigerVNC in Docker
```
