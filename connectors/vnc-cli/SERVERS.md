# Servers

This file records how vnc-cli works with each VNC server, and the quirks of each server. The fleet tests (`go test -p 1 -tags fleet ./...`) check the servers in `fleet/docker-compose.yml`. The servers come from the vncdotool test fleet (MIT, see `NOTICE`). The results are from 2026-10-23, on the development Mac (Docker Desktop, arm64).

All ports listen on 127.0.0.1 only. The container port is 5900, except Selenoid (4444).

| Port | Service | Server | Address |
|---|---|---|---|
| 5931 | `tigervnc` | TigerVNC, no auth | `127.0.0.1::5931` |
| 5932 | `tigervnc-auth` | TigerVNC, VNC password | `127.0.0.1::5932` |
| 5933 | `x11vnc` | x11vnc | `127.0.0.1::5933` |
| 5934 | `vncev` | LibVNCServer event sink | `127.0.0.1::5934` |
| 5935 | `libvncserver-example` | LibVNCServer example | `127.0.0.1::5935` |
| 5940 | `tigervnc-resize` | TigerVNC, for resize and the clipboard event | `127.0.0.1::5940` |
| 5941 | `tigervnc-vencrypt` | TigerVNC, VeNCrypt X509 | `127.0.0.1::5941` |
| 5942 | `tigervnc-tlsvnc` | TigerVNC, TLSVnc and VncAuth | `127.0.0.1::5942` |
| 5944 | `qemu` | QEMU over WebSocket | `ws://127.0.0.1:5944/` |
| 5945 | `qemu-tls` | QEMU over TLS WebSocket | `wss://localhost:5945/` |
| 5946 | `selenoid` | Selenoid, WebSocket to Xvnc | `ws://127.0.0.1:5946/vnc/<session>?password=vncdotool` |
| 5951 | `tigervnc-vencrypt-anon` | TigerVNC, anonymous TLS only | `127.0.0.1::5951` |
| 5952 | `wayvnc` | wayvnc on sway | `127.0.0.1::5952` |

The journey test (`fleet/fleet_test.go`) runs on each server, except 5951: connect, capture, type, click, paste and `clipboard get`, resize, and `watch` (ready, change, and stable). The fleet password is the synthetic value `vncdotool`, and the tests give it by env name.

Encodings: on each server except vncev, a capture with `--encodings` set to rre, corre, hextile, zrle, tight, or copyrect,raw is the same picture as a capture with raw. A server that does not have an encoding sends raw, so this check does not prove that the server used the encoding.

No fleet server sends the clipboard text of one client to the other clients. Only a desktop program that copies text makes a clipboard event. `TestFleetClipboardEvent` sets the CLIPBOARD selection with `xclip` in the `tigervnc-resize` container and expects one `clipboard` event with the text.

## TigerVNC (Xvnc, Debian bookworm)

Version 1.12.0 (`tigervnc-standalone-server 1.12.0+dfsg-8`). Journey: pass on 5931, 5932, 5941, 5942, and 5940.

| Port | Security types | Result |
|---|---|---|
| 5931 | None | Pass. Raw capture matches scene `0.png` pixel for pixel, also with rgb565, bgrx8888, and rgbx8888. |
| 5932 | VncAuth | Pass. |
| 5941 | X509Vnc, X509None, TLSVnc, TLSNone | Pass with X509Vnc and X509None. The CA is `fleet/vencrypt-certs/cert.pem`. A wrong server name fails with "does not match the server name". |
| 5942 | TLSVnc, VncAuth (the Xvnc default) | Pass. The client finds no usable VeNCrypt subtype, connects again, and uses VncAuth. |
| 5951 | TLSVnc, TLSNone | Fails with a clear error: "anonymous TLS is not supported". |

- Go `crypto/tls` has no anonymous Diffie-Hellman cipher suites. The TLSNone, TLSVnc, and TLSPlain subtypes are not supported.
- Xvnc counts each connection that closes before auth and then blocks the address (`BlacklistThreshold`, default 5). The fleet sets 500. A test never sends a wrong password to a fleet server.
- Xvnc takes a resize (ExtendedDesktopSize). The journey resizes only 5940, because a resize changes the screen of each viewer.
- Xvnc sends the clipboard when a program in the desktop copies text, with the extended clipboard. The `watch` clipboard event works. Xvnc does not send the text of `paste` back to any client.

## x11vnc

Version 0.9.16 (`x11vnc 0.9.16-9`, Debian bookworm), over Xvfb at 256x192. Security type None. Journey: pass.

- A resize fails with exit code 30: "the server does not allow a resize".
- The first connection takes about 0.9 s.

## vncev (LibVNCServer 0.9.14)

LibVNCServer 0.9.14, built from source. The event sink writes each event to its log. Security type None. Size 100x100. Journey: pass. The watch step skips.

- The screen shows a text log of the events, so each connection changes the screen. The encoding check does not apply.
- The test reads the log of the container (`docker compose logs vncev`) and finds the key `s` (0x73) and the click at 10,10.
- The first full update takes about 2 s.
- vnc-cli sends `shift-a` as Shift and the keysym `a` (0x61).
- A resize fails with exit code 30: "the server does not allow a resize".

## LibVNCServer example

LibVNCServer 0.9.14, the `example` program. An 800x600 canvas. Security type None. Journey: pass.

- The arrow keys change the size of the screen for each later client. The journey sends no arrow key.
- The example paints where a button is down. In `run`, `drag` holds no button (as in vncdotool), so the watch step uses `mousedown 1`, `drag`, and `mouseup 1`. Paint on old paint changes nothing, so each line goes to a new row.
- One line is a small part of the screen. The watch step uses `--threshold 0.01`.
- A resize fails with exit code 30: "the server does not allow a resize".

## QEMU

Version 7.2 (`qemu-system-x86 1:7.2+dfsg-7+deb12u18`), OVMF 2022.11. The guest is the UEFI shell at 1280x800. Only the WebSocket port is published. Security type None. Journey: pass on `ws://` (5944) and `wss://` (5945).

- `wss://localhost:5945/` needs `--tls-ca-cert fleet/qemu-tls/ca-cert.pem`. The certificate names `localhost`, so the address uses `localhost`, not `127.0.0.1`.
- A resize fails with exit code 30: "the server refused the screen layout".
- A typed command changes a small part of the screen. The watch step uses `--threshold 0.05`. The text cursor blinks, but the stable event still comes.

## Selenoid

Selenoid 1.11.3 (`aerokube/selenoid`). It bridges a WebSocket to an Xvnc (TigerVNC 1.12.0) in the same container. The inner security type is None. Journey: pass. The resize step skips.

- The address works only while a WebDriver session is open. The test opens one with `POST /wd/hub/session` and closes it with `DELETE`.
- With no open session, Selenoid closes the WebSocket at once. vnc-cli then exits with code 20: "version: the server closed the connection".
- The address has the password in the query. vnc-cli writes `?<redacted>` in place of the query in errors.

## wayvnc

wayvnc 0.5.0 with neatvnc 0.5.4 (Debian bookworm), on sway 1.7. VeNCrypt X509Plain with the user `vncdotool`, and `--tls-ca-cert fleet/wayvnc-certs/cert.pem`. Journey: pass. The clipboard step skips.

- wayvnc has no ExtendedDesktopSize. A resize fails with exit code 30: "the server does not support this".
- wayvnc has no extended clipboard. It sends the text of `paste` back to the same connection.
- Defect of the server: wayvnc stops with SIGPIPE (exit 141) when it sends the clipboard and a client closed before. Repro: run `info --session=off`, then `paste TEXT` through the session, four or five times. The journey sends no paste to wayvnc.

## macOS Screen Sharing

Probe on 2026-10-23, macOS 15.7.3 on the development Mac, `127.0.0.1:5900`. The probe read the greeting and the security list, and then closed. It did not try a login.

- The greeting is `RFB 003.889`. The client answers `RFB 003.008`.
- The security types are 30 (Apple Remote Desktop), 33, 36, and 35. Type 2 (VNC password) is not offered, because "VNC viewers may control screen with password" is off.
- vnc-cli uses type 30, with the user and the password of a macOS account.

The ARD login check and the capture check on a Retina screen wait for the user.
