# trebi-connectors

Connectors for [Trebi](https://github.com/trebi-ai/trebi) that live outside the daemon. This repository holds the public catalog, the Go SDK for the `trebi-connector/1` protocol, and Trebi's own channel CLIs.

## Layout

| Folder | What it is |
|---|---|
| `catalog/` | One folder per connector: `trebi-connector.yaml`, `skill/`, and event schemas. Trebi writes and reviews every entry. |
| `schema/` | The manifest JSON Schema. It is vendored byte-identical from `trebi/internal/connectors/manifest/schema.json`. Do not edit it here. |
| `sdk/` | The Go SDK for `trebi-connector/1` (module `github.com/trebi-ai/trebi-connectors/sdk`). See `sdk/README.md`. |
| `channels/` | Channel CLIs: `whatsapp-cli` and `discord-cli`. Each is a normal CLI plus a `serve` command that speaks the protocol. See `channels/README.md`. |
| `tools/catalogctl/` | Validates the entries, builds the snapshots, and writes and signs `index.json`. |
| `scripts/` | `validate.sh` checks the catalog. `publish.sh` uploads the catalog to R2. |

## Add or change a catalog entry

1. Add or edit `catalog/<name>/trebi-connector.yaml`. The folder name is the `name` field.
2. Put the skill in `catalog/<name>/skill/SKILL.md`. A channel entry carries a copy of `channels/<cli>/SKILL.md`, and the two must stay the same.
3. Bump `version` for each change. A published version is immutable.
4. Run `scripts/validate.sh`. Add `--conformance` to check the `serve --sandbox` command of each protocol entry.

## Publish flow

- A PR that changes `catalog/` or `schema/` runs `.github/workflows/catalog.yml`. It validates every entry and runs the conformance check on each protocol entry.
- A push to `main` runs the same workflow and publishes to the R2 bucket `trebi-catalog`, served at `https://catalog.trebi.ai/v1/`. The job runs in the environment `catalog-publish`.
- The layout in the bucket is `v1/index.json`, `v1/index.json.sig`, `v1/snapshots/<name>/<version>.tar.gz`, and `v1/icons/<name>.svg`.
- A snapshot is a deterministic tar.gz of the entry folder. `publish.sh` never writes a snapshot key again. If the key exists with other bytes, the run fails.
- The signature is ed25519 over the bytes of `index.json`, in base64. The daemon embeds the public key.
- An entry with a `github_release` install block is held back until its release assets exist and match `SHA256SUMS`. The previous version of that entry stays in the index.

## Release a channel CLI

1. Push the tag `channels/<cli>/vX.Y.Z`. `.github/workflows/channel-release.yml` builds the archives with GoReleaser and creates the GitHub release.
2. The release job then starts the catalog workflow on `main`, which publishes the entry that was held back.
3. Bump the catalog entry version to the same version in the same PR as the code change.

## Build and test

Each Go module is independent. Build with `GOWORK=off`.

```bash
(cd sdk && go test ./...)
(cd channels/discord-cli && go test ./...)
(cd channels/whatsapp-cli && CGO_ENABLED=1 go test -tags sqlite_fts5 ./...)
(cd tools/catalogctl && go test ./...)
scripts/validate.sh --conformance
```

## License

The repository has no root license yet. `channels/whatsapp-cli` keeps its upstream MIT `LICENSE`.
