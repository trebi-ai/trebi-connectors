# host-cli

A small Go CLI that reads the health of a Linux server over SSH: CPU, memory, swap, disks, load, the top processes, and the systemd services. It runs one shell script on the server (`internal/probe`) that reads `/proc`, `df`, `ps`, and `systemctl`. It needs no root and installs nothing. Its `serve` command is the Trebi adapter that reads the server at an interval. The catalog entry `catalog/host` uses it. The skill is `catalog/host/skill/SKILL.md`.

## Build

```bash
go build -o host-cli .
./scripts/build.sh            # writes ./build/host-cli with version metadata
```

## Use on its own

`host-cli --host <host[:port]> --user <user> [--key <file>] sample [--json]` reads the server once.

| Env | What it is |
|---|---|
| `SSH_HOST`, `SSH_USER` | The server and the user. |
| `SSH_KEY` | The text of a private key with no passphrase. |
| `SSH_KEY_FILE` | The path of a key file (outside Trebi only). |
| `SSH_AUTH_SOCK` | The SSH agent socket. |
| `SSH_HOST_KEY` | The pinned server key. Without it, the tool checks `~/.ssh/known_hosts`. |
| `HOST_EVERY` | The interval of `serve`. The default is `60s`; the minimum is `10s`. |
| `HOST_CPU_ABOVE`, `HOST_MEMORY_ABOVE`, `HOST_DISK_ABOVE` | The limits in percent. The default is `90`. |
| `HOST_SAMPLES` | `yes` or `no`: send a `sample` event at each check. |

Outside Trebi, the key comes from `--key`, `SSH_KEY`, `SSH_KEY_FILE`, the agent, then `~/.ssh/id_ed25519`, `id_ecdsa`, and `id_rsa`. The state goes to `~/.cli-tools/host-cli/`.

## Use with Trebi

Trebi runs `host-cli serve` with `TREBI_STATE_DIR` set. The values come only from the inputs of the connection. Trebi does not give its own `SSH_AUTH_SOCK` to a connector, so the agent socket is an input.

- With no server, user, or key, `serve` sends `status` `auth_required` with reason `missing_input`.
- `--host`, `--user`, and `--key` fail with "Trebi sets this value".
- With no `SSH_HOST_KEY`, the first server key goes to `known_hosts` in the state folder. A different key later gives `status` `error`.
- A refused key gives `status` `auth_required` with reason `revoked`.
- The state folder holds `known_hosts` and `trebi-sdk-state.json` (the values and the limits of `sdk.StateWatcher`).
- `serve --sandbox` reads a fake SSH server (`internal/fakessh`) in the same process with a 2 s interval. Each third reading has a full disk and a failed service.

The rules for all connector programs are in "Adapter folder contract" in `../../CLAUDE.md`.
