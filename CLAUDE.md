# CLAUDE.md

Instructions for humans and coding agents who work in `trebi-connectors`.

## What this repository is

`trebi-connectors` holds the connectors of [Trebi](https://github.com/trebi-ai/trebi) that live outside the daemon. It is public. It has three parts:

- The public catalog. Trebi writes and reviews every entry. CI signs and publishes it to `catalog.trebi.ai`.
- The Go SDK for the `trebi-connector/1` protocol.
- The source of the programs that the catalog builds: `whatsapp-cli`, `discord-cli`, `github-cli`, `linear-cli`, `notion-cli`, and `mstodo-cli`.

The daemon owns the protocol, the manifest schema, and the conformance checks. This repository implements them. `README.md` has the publish flow and the SDK release steps.

## Layout

| Folder | What it is |
|---|---|
| `catalog/<name>/` | One entry: `trebi-connector.yaml`, `skill/SKILL.md`, and event schemas in `schemas/`. |
| `schema/` | The manifest JSON Schema, vendored byte-identical from `trebi/internal/connectors/manifest/schema.json`. Do not edit it here. |
| `sdk/` | The Go SDK (module `github.com/trebi-ai/trebi-connectors/sdk`). `sdk/contract/` is vendored from `trebi/internal/connectors/protocol/testdata/contract/`. Do not edit it here. |
| `connectors/<cli>/` | The source of one program: a normal CLI plus a `serve` command that speaks the protocol. `scripts/build.sh [version] [output]` builds it. |
| `tools/catalogctl/` | Validates the entries, lists the pending builds, and writes and signs `index.json`. |
| `scripts/` | `validate.sh` checks the catalog. `validate.sh --conformance` runs `trebi connector conformance` on each entry. `publish.sh` uploads to R2. |
| `.claude/skills/connector-authoring/` | The skill with the full steps to build a connector program. |

## Rules for a catalog entry

- The folder name is the `name` field of the manifest.
- `skill/SKILL.md` is the only copy of the skill, also for a program in `connectors/`.
- Bump `version` for each change to the entry or to its program. A published version is immutable.
- A required input has a `label`. The label is the name that the user sees, not an env var name.
- Run `scripts/validate.sh` before a PR. For an entry with a program, also run `scripts/validate.sh --conformance <name>`.
- Write all prose in ASD-STE100 Simplified Technical English. Write one paragraph per line, with no hard wraps.

## Adapter folder contract

These rules are for every connector program. They are copied word for word from the Trebi plan for connector connections. `.claude/skills/connector-authoring/SKILL.md` has the full steps and the checklist before a PR.

1. **Durable data goes in `TREBI_STATE_DIR`.** This is the login session, the message store, the dedupe files, and downloaded attachments that the user can refer to later. The program writes no durable data to another place when this variable is set.
2. **Data that can be built again goes in `TREBI_CACHE_DIR`.** This is temp files, thumbnails, and indexes that the program can make from the state. The daemon can delete this folder when the process is stopped.
3. **Logs go to stderr.** The daemon writes them to `logs/stream.jsonl`, shows them in the UI, and applies rotation. The program writes no log files. Stdout is for the protocol only.
4. **Paths in state are relative.** A file in the state folder never holds an absolute path. A restore can put the folder under a different home. A path in an event (for example an attachment `path`) can be absolute, because the daemon uses it at once.
5. **Empty state is normal.** A new connection and a restore without state start with an empty folder. The program then sends `status` `auth_required` (or `connecting` when it needs no login). It does not fail.
6. **Old state is normal.** A restore can give state from an older version of the program. The program upgrades its own state format at start. State from a newer version gives `status` `error` with a clear message. It does not delete data.
7. **One writer.** The daemon runs one `serve` process for each connection. `serve` holds a lock in the state folder. A CLI command of the same program that runs in a job forwards to `serve` through a socket in the state folder, or opens the state read-only. Socket names are short: the full path stays under 104 bytes. The connection folder path is at most 78 bytes on a normal home, so `state/` plus a name of up to 16 bytes is safe.
8. **Clean stop.** On `shutdown`, the program flushes and closes its files (for SQLite: a WAL checkpoint and close). A backup stops the connection, copies the folder, and starts it again, so the copy is always consistent.
9. **Trebi mode (D14).** When `TREBI_STATE_DIR` is set, settings come only from the env and the state folder. No home config file, no `.env` search, no second env name, no fallback store. A missing setting gives `status` `auth_required` that names the input.
10. **Standalone first.** A connector program is a normal tool that works with no Trebi. When `TREBI_STATE_DIR` is not set, it uses its own defaults, flags, env names, and config files (for example `~/.whatsapp-cli` or `~/.cli-tools/discord-cli/config.json`). Trebi adds a mode; it removes nothing from the standalone tool. Write the choice between the two modes in one place, the config package of the program, so that no other code reads the home folder.
    - **Precedence.** When `TREBI_STATE_DIR` is set, the Trebi values win over everything: over the standalone env names, the config files, the `.env` files, and the flags that select a store or a credential.
    - **Flags in Trebi mode.** A flag that selects a store or a credential (`--store`, `--token`) fails in Trebi mode with the message "Trebi sets this value". It does not quietly win, and it is not quietly ignored. So an agent in a run cannot point the program at the personal store of the user.
11. **Commands that write settings.** A command that stores a setting outside Trebi (for example `discord-cli auth set`) fails in Trebi mode with the message "Set this value in Trebi". The value must come from the connection inputs, not from a file that the program writes.
