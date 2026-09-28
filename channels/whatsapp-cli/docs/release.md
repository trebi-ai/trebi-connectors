# Release

## GitHub Release Artifacts

`whatsapp-cli` uses GoReleaser (`.goreleaser.yaml` for macOS, `.goreleaser-linux-windows.yaml` for linux/windows) and the GitHub Actions workflow `.github/workflows/release.yml`.

To cut a release:

1. Tag and push:
   - `git tag vX.Y.Z`
   - `git push origin vX.Y.Z`
2. Wait for the GitHub Actions “release” workflow to publish the release artifacts.

To re-release an existing tag, run the workflow manually and pass the tag (e.g. `v0.1.0`).

Expected macOS artifact name (used by the tap updater):

- `whatsapp-cli-macos-universal.tar.gz`

Other artifacts:

- `whatsapp-cli-linux-<arch>.tar.gz`
- `whatsapp-cli-windows-<arch>.zip`

## Homebrew Tap

The tap formula lives in `../homebrew-tap/Formula/whatsapp-cli.rb`.

Once a release exists, update the tap formula by running the `Update Formula` workflow in the tap repo with:

- `formula`: `whatsapp-cli`
- `tag`: `vX.Y.Z`
- `repository`: `steipete/whatsapp-cli`
