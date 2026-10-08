# trebi-connectors

Connectors for [Trebi](https://github.com/trebi-ai/trebi) that live outside the daemon. This repository holds the public catalog, the Go SDK for the `trebi-connector/1` protocol, and the source of the CLIs that Trebi builds. `CLAUDE.md` has the rules for an entry and the adapter folder contract. `.claude/skills/connector-authoring/SKILL.md` has the steps to build a connector program. `CONTRIBUTING.md` tells how to open a pull request for a new connector.

## Layout

| Folder | What it is |
|---|---|
| `catalog/` | One folder per connector: `trebi-connector.yaml`, `skill/`, and event schemas. Trebi writes and reviews every entry. |
| `schema/` | The manifest JSON Schema. It is vendored byte-identical from `trebi/internal/connectors/manifest/schema.json`. Do not edit it here. |
| `sdk/` | The Go SDK for `trebi-connector/1` (module `github.com/trebi-ai/trebi-connectors/sdk`). See `sdk/README.md`. |
| `connectors/` | The source of the programs that the catalog builds and hosts: `whatsapp-cli` and `discord-cli`. Each is a normal CLI plus a `serve` command that speaks the protocol. See `connectors/README.md`. |
| `tools/catalogctl/` | Validates the entries, builds the snapshots, and writes and signs `index.json`. |
| `scripts/` | `validate.sh` checks the catalog. `publish.sh` uploads the catalog to R2. |

## Add or change a catalog entry

1. Add or edit `catalog/<name>/trebi-connector.yaml`. The folder name is the `name` field.
2. Put the skill in `catalog/<name>/skill/SKILL.md`. This is the only copy of the skill, also for a CLI in `connectors/`.
3. Bump `version` for each change. A published version is immutable.
4. Run `scripts/validate.sh`. Add `--conformance` to run `trebi connector conformance --manifest catalog/<name>` on each entry. It needs a `trebi` with that command on `PATH`.

## Publish flow

- A PR that changes `catalog/` or `schema/` runs `.github/workflows/catalog.yml`. It validates every entry. It downloads the newest public `trebi` release, checks it against `SHA256SUMS`, and runs the conformance check on each entry.
- A push to `main` runs the same workflow and publishes to the R2 bucket `trebi-catalog`, served at `https://catalog.trebi.ai/v1/`. The job runs in the environment `catalog-publish`.
- The layout in the bucket is `v1/index.json`, `v1/index.json.sig`, `v1/snapshots/<name>/<version>.tar.gz`, `v1/bin/<name>/<version>/<bin>_<os>_<arch>.tar.gz`, and `v1/icons/<name>.svg`.
- A snapshot is a deterministic tar.gz of the entry folder. `publish.sh` never writes a snapshot key again. If the key exists with other bytes, the run fails.
- The signature is ed25519 over the bytes of `index.json`, in base64. The daemon embeds the public key.

## Programs from `connectors/`

An entry with `install: {catalog: true, bin: <cli>}` gets its program from the catalog. The source is `connectors/<cli>/`, and `connectors/<cli>/scripts/build.sh [version] [output]` builds it for the host platform.

1. Change the code in `connectors/<cli>/` and bump `version` in `catalog/<name>/trebi-connector.yaml` in the same PR.
2. On `main`, the `plan` job of `catalog.yml` lists each `install.catalog` entry whose version is not in the published index.
3. The `build` job builds each platform on a native runner, so a CGO program needs no cross compiler. It stamps the entry version into the program.
4. The `publish` job puts the archives in `v1/bin/`, and the index lists the digest of each archive. The daemon checks that digest before it installs the program.

There are no release tags and no GitHub releases for these programs. A published archive is immutable, like a snapshot.

A developer can also use `go install`. The program then shows the module version, not the entry version.

```bash
go install github.com/trebi-ai/trebi-connectors/connectors/discord-cli@latest
CGO_ENABLED=1 go install -tags sqlite_fts5 github.com/trebi-ai/trebi-connectors/connectors/whatsapp-cli/cmd/whatsapp-cli@latest
```

## The SDK

The CLIs in `connectors/` require the SDK by its tag `sdk/vX.Y.Z`. The committed `go.work` builds them against the SDK in the same commit, so a PR can change the SDK and a CLI together. To release an SDK change:

1. Merge the SDK change.
2. Push the git tag `sdk/vX.Y.Z` on that commit. There is no GitHub release.
3. Bump the SDK require in `connectors/*/go.mod` and the version of the `replace` in `go.work`.

## Build and test

```bash
(cd sdk && go test ./...)
(cd connectors/discord-cli && go test ./...)
(cd connectors/whatsapp-cli && CGO_ENABLED=1 go test -tags sqlite_fts5 ./...)
(cd tools/catalogctl && GOWORK=off go test ./...)
scripts/validate.sh --conformance
```

## License

The repository has no root license yet. `connectors/whatsapp-cli` keeps its upstream MIT `LICENSE`.
