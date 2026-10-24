---
name: vnc-cli
description: >
  See and control the screen of another computer over VNC (macOS Screen Sharing, Linux, Windows, QEMU and Proxmox consoles). Use when the user wants a picture of a remote screen, wants to type or click on it, wants to wait until something shows on it, or wants to start work when an area of the screen changes, a dialog shows, or the clipboard changes.
  Triggers: "remote screen", "screen sharing", "vnc", "take a screenshot of the remote computer", "type on the other computer", "click on the remote screen", "wait for the login screen", "watch the screen", "screen changed", "remote clipboard", "proxmox console", "qemu console".
---

# vnc-cli — the screen of another computer

Go CLI that connects to a VNC server. It takes pictures of the screen, types, moves and clicks the pointer, waits for a picture or for a quiet screen, sets and reads the clipboard, and watches areas of the screen for changes. It keeps one connection open in the background, so each command after the first takes milliseconds. Binary: `vnc-cli`. It is a Go port of vncdotool, and it plays vncdotool `.vdo` scripts.

## Settings

In Trebi, the connection has these inputs:

- "Computer address" (`VNC_SERVER`): the address of the computer. See "Addresses".
- "User name" (`VNC_USER`): the account name for a Mac with a user login (Apple Remote Desktop), or for VeNCrypt Plain. Leave it empty for a screen password only.
- "Password" (`VNC_PASSWORD`, secret): the screen sharing password, or the account password of a Mac.
- "Watch the screen for changes" (`VNC_WATCH`): `no` (default) keeps the connection for actions only. Then the connection sends no screen, clipboard, or bell events, only `state.changed` and `state.recovered`. `yes` turns on the events of "Events in Trebi".
- "Watched areas" (`VNC_AREAS`): `NAME=X,Y,W,H` in screen pixels, separated by `;`. Empty watches the whole screen as one area named `screen`.
- "Quiet time before a stable event" (`VNC_SETTLE`): default `700ms`.
- "Attach a picture to each event" (`VNC_IMAGES`): `yes` (default) adds a PNG of the area to each `screen.stable` event.

In Trebi, a command in a job uses the connection of the Trebi connection. It does not connect by itself, and it does not need the password. `--server`, `--target`, `--user`, and `--password-env` fail with "Trebi sets this value". `config set` and `config rm` fail with "Set this value in Trebi".

On its own, `vnc-cli` takes `-s ADDRESS`, `--user`, and `-p ENV_NAME` (the name of an env variable that holds the password), or `VNC_SERVER`, `VNC_USER`, and `VNC_PASSWORD`. Named servers live in `~/.cli-tools/vnc-cli/config.toml` (`config set NAME --address A [--login U] [--secret-env NAME] [--default]`, then `-t NAME`). There is no `--password` flag. When the server asks for a password that is not set and stdin is a terminal, vnc-cli asks for it.

## Addresses

| Form | Meaning |
|---|---|
| `HOST`, `HOST:N` | Display N (port 5900+N) when N is less than 100, else port N. |
| `HOST::PORT` | Port PORT. |
| `[v6]`, `[v6]:N`, `[v6]::PORT` | An IPv6 address. |
| `/path`, `unix:/path` | A Unix socket. |
| `ws://...`, `wss://...` | A WebSocket (noVNC, Proxmox). The path and the query stay. |

## Commands

| Command | What it does |
|---|---|
| `capture [-r X,Y,W,H] [--cursor none\|local\|server] FILE` | Save the screen, or an area of it, as png, jpg, gif, or bmp. `-` writes PNG to stdout. The pointer is not in the picture by default. `--cursor local` draws it. `--cursor server` lets the server draw it on a second connection, so it does not work in Trebi. |
| `info` | Show the desktop name, the size, the pixel format, and the protocol. |
| `resize WIDTH HEIGHT` | Ask the computer for another screen size. Exit code 30 when the server refuses it or cannot resize. |
| `key KEY...` | Press keys or combos: `enter`, `esc`, `tab`, `ctrl-c`, `ctrl-alt-del`, `cmd-space`, `f5`, `Down`, or a keysym such as `0xff0d`. |
| `key down KEY`, `key up KEY` | Hold and release one key. |
| `type TEXT\|-` | Type text. `--enter` presses enter after it. `--human` types with random pauses. `--force-caps` holds shift for upper case letters. |
| `type --env NAME`, `type --file PATH` | Type a secret from an env variable or a file. The value never goes into the command line. |
| `move X Y`, `click [BUTTON]` | Move the pointer, and click (1 left, 2 middle, 3 right). |
| `mouse down\|up BUTTON`, `drag X Y [--step N]` | Hold and release a button, or drag to X Y. |
| `scroll DX DY` | Turn the wheel. DY > 0 scrolls down. |
| `paste TEXT\|-\|--file PATH`, `clipboard get` | Set or read the clipboard of the computer. |
| `expect FILE [-r X,Y]` | Wait until the screen, or the area at X,Y, matches a picture. Use `--timeout`. |
| `stable SECONDS [-r X,Y,W,H]` | Wait until the screen, or the area, shows no change for SECONDS. |
| `run FILE\|-`, `run -- COMMAND...` | Play a vncdotool `.vdo` script, or a chain of commands, on one connection. |
| `watch ...` | Write a JSON line for each change of the screen. See "Watch". |
| `session start\|status\|stop [--all]\|list` | Control the background connection (not in Trebi). |
| `serve [--sandbox]` | Serve the `trebi-connector/1` protocol for Trebi. `--sandbox` watches a fake screen in the same process. |

Global flags: `--timeout D` (exit 40 after D), `--delay N|MIN-MAX` (milliseconds between actions), `--fuzz N` and `--blur N` (how close a match of `expect` and `stable` must be), `-j/--json`, `-v/--verbose`, `--session auto|off`, `--idle-timeout D`, `--encodings LIST`, `--jpeg-quality 0-9`, `--pixel-format bgrx8888|rgbx8888|rgb565`, `--tls-ca-cert FILE`, `--tls-insecure-skip-verify`.

Always set `--timeout` on `expect` and `stable`. Without it they wait for ever.

## Secrets

Type a password only with `type --env NAME` or `type --file PATH`. Never put a secret in the text of `type`, `paste`, or `run`, because the command line is visible to other programs and goes into logs. In a Trebi job, give the secret to the job as an env variable and type it by name:

```bash
vnc-cli type --env DB_PASSWORD --enter
```

## The background session

The first command starts a background session that keeps the connection open, and the next commands use it. A capture then takes some milliseconds. The session connects again when the connection drops, and a command waits for it up to `--timeout`. A failed login stops the session, so that a wrong password is not sent again. The session stops after `--idle-timeout` (default 5m) with no command. `--session off` connects for one command only.

In Trebi there is no separate session: the Trebi connection keeps the connection open, and every command in a job goes through it. When the connection does not run in Trebi, a command fails with exit 10 and "the connection to this computer does not run in Trebi".

The session asks for a shared connection, so the other viewers of the computer stay connected.

## Watch

`vnc-cli watch` writes one JSON object per line. The events and fields are those of `glkvm-cli watch`.

```bash
vnc-cli watch -a clock=1700,0,220,40 -a dialog=600,300,700,400 --settle 1s
vnc-cli watch --once -d 2m        # exit 0 at the first stable event, exit 1 after 2m with none
```

| Event | When |
|---|---|
| `ready` | The start. It gives the areas, the reference size `ref`, and the screen `picture`. |
| `change` | An area starts to change: `changed_pct`, `changed_px`, `bbox`. |
| `stable` | The area has not changed for `--settle`: `changing_ms`, `bbox`. This state is the new base. |
| `clipboard` | The clipboard changed: `text`. |
| `bell` | The computer rang the bell. |
| `resize` | The screen size changed: `size`. A new `ready` follows; the areas scale with the screen. |
| `cursor` | The pointer shape changed. Only with `--events ...,cursor`. |
| `disconnected`, `connected` | The connection dropped, and came back. A new `ready` follows. |

Flags: `-a [NAME=]X,Y,W,H` (repeat), `--ref W[xH]` (the size that the area coordinates refer to), `--threshold 0.5` (percent), `--min-pixels 16`, `--pixel-delta 24` (brightness 0-255), `--settle 700ms`, `--max-fps 10`, `--events LIST`, `--save DIR` (a PNG with each change and stable), `--once`, `-d D`.

A clock, a blinking text cursor, or an animation in an area gives events each time it changes. Make the areas small and keep them away from these parts. To find the coordinates, take a capture first and read the pixel positions from it.

## Events in Trebi

The room is the computer: its address, with the desktop name as the room name.

The screen, clipboard, and bell events come only when "Watch the screen for changes" is `yes`. With `no`, a job that runs on `screen.stable` never starts. Then use `vnc-cli stable` or `vnc-cli expect` in the job to wait for the screen.

- `screen.changed`: a watched area starts to change. `data` has the watch fields (`area`, `rect`, `changed_pct`, `changed_px`, `bbox`).
- `screen.stable`: the area stopped changing. `data` adds `changing_ms`. With "Attach a picture to each event" set to yes, the attachment is a PNG of the area. Trebi keeps the newest 200 pictures.
- `clipboard.changed`: `data.text` is the new clipboard text.
- `bell`: the computer rang the bell.
- `screen.resized`: `data.size` has `width` and `height`.
- `state.changed` with key `reachable`: Trebi cannot reach the computer. `state.recovered`: Trebi can reach it again.

A wrong password stops the connection and asks for new inputs, so that the computer does not block Trebi. A computer that Trebi cannot reach gives an error, and Trebi tries again with a longer wait each time, up to 30s.

## Exit codes

| Code | Meaning |
|---|---|
| 0 | All commands completed. |
| 1 | An unexpected error, or `watch --once -d` with no stable event. |
| 2 | A bad command line. |
| 3 | The login failed, usually a wrong password. Do not try again with the same password. |
| 10 | vnc-cli cannot connect. |
| 11 | The server closed the connection. |
| 20 | A protocol error, or a TLS certificate that is not trusted. |
| 30 | A command failed, for example an area that is not on the screen. |
| 40 | `--timeout` ended. |

## Servers

| Server | Notes |
|---|---|
| macOS Screen Sharing | Uses the Apple Remote Desktop login: set the user name and the account password. A plain screen password works only when "VNC viewers may control screen with password" is on. The Mac does not send its clipboard, so `clipboard get` is empty and no `clipboard` event comes; `paste` works. The pointer is never in a capture. `key shift-a` gives "a": use `type A` for upper case. |
| TigerVNC, TurboVNC | VNC password and VeNCrypt with X509. TLSNone, TLSVnc, and TLSPlain (anonymous TLS) are not supported. Xvnc blocks an address after 5 failed logins. |
| x11vnc, LibVNCServer, wayvnc | VNC password. |
| QEMU, Proxmox | No password, VNC password, or a WebSocket address with a ticket. |
| TightVNC, UltraVNC | VNC password. |
| KasmVNC | Not supported. |

## Limits

- The clipboard is UTF-8 when the server has the extended clipboard (TigerVNC 1.10 and later). On other servers it is Latin-1, and other characters do not go through.
- `type` sends keysyms. The keyboard layout of the remote computer decides the characters, so text that is not Latin can come out wrong. On QEMU, `key` also sends the scan code, so keys do not depend on the layout.
- No file transfer, no sound, and no recording.
- A capture is the current screen of the session. After an action, use `expect` or `stable` before you capture, so the screen has time to change.

## Examples

```bash
vnc-cli -s mac-mini.local --user admin -p MAC_PW capture screen.png
vnc-cli -s 10.0.0.5:1 run -- move 400 300 click 1 type "hello" key enter
vnc-cli -s 10.0.0.5:1 --timeout 60s expect login.png && vnc-cli -s 10.0.0.5:1 type --env PW --enter
vnc-cli -s 10.0.0.5:1 --timeout 30s stable 2 -r 0,0,800,600 && vnc-cli -s 10.0.0.5:1 capture -r 0,0,800,600 area.png
vnc-cli -s 'wss://pve.example.com:8006/api2/json/nodes/pve/qemu/100/vncwebsocket?port=5900&vncticket=...' info
```
