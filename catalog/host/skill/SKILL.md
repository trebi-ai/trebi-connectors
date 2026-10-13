---
name: host-cli
description: >
  Linux server health over SSH. Use when the user wants to know the CPU, memory, disk, or load of a server, see the busiest processes or the failed services, or start work when a server goes down, a disk fills up, or a service fails.
  Triggers: "server health", "disk full", "cpu of the server", "is the server up", "failed service", "watch my server", "load average".
---

# host-cli — Linux server health over SSH

Go CLI that logs in to a Linux server over SSH and reads its CPU, memory, swap, disks, load, the busiest processes, and the systemd services. It needs no admin rights and installs nothing on the server. In Trebi it reads the server at an interval and sends events. Binary: `host-cli`.

## Settings

In Trebi, the connection has these inputs:

- "Server address" (`SSH_HOST`): the host name or IP address. Add `:port` for a port other than 22.
- "User name" (`SSH_USER`): the user that Trebi logs in as.
- "Private key" (`SSH_KEY`, secret): the full text of a private key with no passphrase. A key with a passphrase does not work: use the key agent.
- "Key agent" (`SSH_AUTH_SOCK`): the path of an SSH agent socket. Leave it empty to use the key agent of this computer. The program uses the agent when there is no private key, and also as a second try.
- "Server key" (`SSH_HOST_KEY`): the public key of the server, as in `known_hosts` or `ssh-keyscan`. When it is empty, Trebi trusts the first key that it sees and keeps it in the state folder (`known_hosts`). A different key later stops the connection with an error.
- "Check every" (`HOST_EVERY`): the interval. The default is `60s`. The minimum is `10s`.
- "CPU alert above", "Memory alert above", "Disk alert above" (`HOST_CPU_ABOVE`, `HOST_MEMORY_ABOVE`, `HOST_DISK_ABOVE`): the limits in percent. The default is `90`.
- "Send each reading" (`HOST_SAMPLES`): `yes` (default) sends a `sample` event at each check. `no` sends only `state.changed` and `state.recovered`.

On its own, `host-cli` takes `--host`, `--user`, and `--key <file>`, or the env names `SSH_HOST`, `SSH_USER`, `SSH_KEY`, and `SSH_KEY_FILE`. It then tries the SSH agent and `~/.ssh/id_ed25519`, `id_ecdsa`, and `id_rsa`, and checks the server key in `~/.ssh/known_hosts`. In Trebi, these flags fail with "Trebi sets this value".

## Commands

| Command | What it does |
|---|---|
| `host-cli sample [--json]` | Read the server once and print the numbers, the disks, the top processes, and the failed services. |
| `host-cli serve [--sandbox]` | Serve the `trebi-connector/1` protocol for Trebi. `--sandbox` reads a fake server in the same process. |
| `host-cli --version` | Show the version. |

## Events

The room is the server: its address, with no port when the port is 22. The room name is the host name that the server gives.

- `sample`: one reading. `text` is a short summary. `data.metrics` has the numbers by key: `cpu.used_pct`, `cpu.count`, `mem.used_pct`, `mem.total_bytes`, `mem.available_bytes`, `swap.used_pct`, `load.1`, `load.5`, `load.15`, `disk[<mount>].used_pct`, `disk[<mount>].free_bytes`, `units.total`, and `units.failed`. `data` also has `disks`, `processes` (at most 10, the most CPU first), and `failed_units`.
- `state.changed`: a value went past its limit. `data` has `key`, `old`, `new`, and `threshold` (`op` and `value`). The keys are `reachable` (1 or 0; below 1 means that Trebi cannot reach or read the server), `cpu.used_pct`, `mem.used_pct`, `disk[<mount>].used_pct`, and `unit[<service>].failed` (1 when the service failed).
- `state.recovered`: the same value came back. It has the same `data` fields.

A limit gives one `state.changed` when the value goes past it and one `state.recovered` when it comes back. A value that stays past the limit gives no new event. The state survives a restart, so a restart does not send an alert again.

When the server refuses the key, the connection asks for a new login. When the server key changes, the connection shows an error and sends no data.

## Examples

```bash
host-cli --host web-1.example.com --user ops sample
host-cli --host 10.0.0.5:2222 --user ops --key ~/.ssh/monitor sample --json
```
