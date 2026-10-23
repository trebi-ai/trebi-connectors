# Servers

This file records how vnc-cli works with each VNC server, and the quirks of each server. The fleet tests (`go test -tags fleet ./vnc/`) check the servers in `fleet/docker-compose.yml`.

## TigerVNC (Xvnc, Debian bookworm)

| Port | Security types | Result |
|---|---|---|
| 5931 | None | Pass. Raw capture matches scene `0.png` pixel for pixel, also with rgb565, bgrx8888, and rgbx8888. |
| 5932 | VncAuth | Pass. |
| 5941 | X509Vnc, X509None, TLSVnc, TLSNone | Pass with X509Vnc and X509None. The CA is `fleet/vencrypt-certs/cert.pem`. A wrong server name fails with "does not match the server name". |
| 5942 | TLSVnc, VncAuth (the Xvnc default) | Pass. The client finds no usable VeNCrypt subtype, connects again, and uses VncAuth. |
| 5951 | TLSVnc, TLSNone | Fails with a clear error: "anonymous TLS is not supported". |

- Go `crypto/tls` has no anonymous Diffie-Hellman cipher suites. The TLSNone, TLSVnc, and TLSPlain subtypes are not supported.
- Xvnc counts each connection that closes before auth and then blocks the address (`BlacklistThreshold`, default 5). The fleet sets 500. A test never sends a wrong password to a fleet server.

## macOS Screen Sharing

Probe on 2026-10-23, macOS 15.7.3 on the development Mac, `127.0.0.1:5900`. The probe read the greeting and the security list, and then closed. It did not try a login.

- The greeting is `RFB 003.889`. The client answers `RFB 003.008`.
- The security types are 30 (Apple Remote Desktop), 33, 36, and 35. Type 2 (VNC password) is not offered, because "VNC viewers may control screen with password" is off.
- vnc-cli uses type 30, with the user and the password of a macOS account.

The ARD login check and the capture check on a Retina screen wait for the user.
