# github-cli

`github-cli` is a small command line tool for GitHub. It shows the account of a token, lists repositories, the recent events of a repository, and its webhooks, and runs the GitHub operations of Trebi. Its `serve` command speaks the `trebi-connector/1` protocol, so Trebi can start work when something happens in a repository or needs the user.

## Build

`scripts/build.sh [version] [output]` builds for `GOOS` and `GOARCH` (default: this machine). The output folder gets five files, and the catalog archive holds each of them:

| File | What it is |
|---|---|
| `github-cli` | The program. |
| `github-op` | A copy of the program. Trebi runs the operations with this name, because an operation program must not also be a run command. |
| `gh` | A copy of the program. With this name, the program runs `gh-bin` with the login of the connection (see "The gh shim"). |
| `gh-bin` | The official gh, pinned to `GH_VERSION` in `scripts/build.sh`. The script checks its SHA-256. |
| `gh-LICENSE` | The licence of gh (MIT). |

The copies are files, not links, because Trebi installs only regular files. The script keeps the gh downloads in `$RUNNER_TOOL_CACHE/gh/<version>`, or in `build/cache/gh/<version>`. When the machine can run the target, the script runs `gh --version` of the output.

To move to a new gh, change `GH_VERSION` and the four SHA-256 values in `gh_sha256` from the `gh_<version>_checksums.txt` of the gh release. Then bump the version of the catalog entry.

## Use on its own

Give the tool a GitHub token. The tool reads the token in this order: the `--token` flag, the `GITHUB_TOKEN` env, the `GH_TOKEN` env, then `~/.cli-tools/github-cli/config.json` with the content `{"token": "ghp_…"}`.

```bash
github-cli whoami
github-cli repos list
github-cli --json events list octo/app
github-cli hooks list octo/app
echo '{"query":"is:pr is:open review-requested:@me"}' | github-cli op search
github-cli --version
```

Outside Trebi, `gh` of the build folder runs `gh-bin` with no change.

## Use with Trebi

Add the GitHub connector from the catalog. Trebi runs `github-cli serve` with `TREBI_STATE_DIR`. In this mode the token comes from the first of:

1. The input "Personal access token" (`GITHUB_TOKEN`).
2. `auth.json` in the state folder, which a login writes. `{"source":"device","token":"…"}` is a login with "Sign in with GitHub". `{"source":"gh"}` is a login with "Use the GitHub login on this machine": the program runs `gh auth token -h github.com` of the user each time it needs the token, and it keeps no copy. It skips its own folder in `PATH`, and it gives the child no token env.

The `--token` flag fails with "Trebi sets this value". A logout deletes `auth.json`. It never runs `gh auth logout`.

"Sign in with GitHub" uses the GitHub device flow with the Trebi OAuth app and the scopes `repo`, `read:org`, and `admin:repo_hook`. The build sets the app id with `TREBI_GITHUB_CLIENT_ID`. A build without it has no device login.

### The gh shim

When the program runs as `gh` with `TREBI_STATE_DIR` set, it runs `gh-bin` with:

- `GH_TOKEN` from the token of the connection, and `GH_HOST=github.com`. It removes the other token and host env names.
- `GH_CONFIG_DIR` in the state folder, with `git_protocol: https` and prompts off. No pager, no colors, no update notice.
- A git credential helper for `https://github.com` and `https://gist.github.com` that is this gh, so `git clone` and `git push` use the connection.

It refuses `gh auth login|logout|refresh|switch|setup-git|token`, `gh extension install|upgrade`, and `gh codespace` with exit code 2. With no login, it stops with exit code 4.

### Events

Watch a repository in the connection to get its events. For each watched repository, the adapter picks the mode:

- With Trebi Cloud, a login that is an admin of the repository, and a token that can add webhooks, the adapter adds one webhook with the hosted URL and secret, and checks the `x-hub-signature-256` header of each delivery. When GitHub refuses the webhook, the adapter falls back to the next mode.
- Else it reads the repository events every minute or more, with `If-None-Match`, and the Actions runs when `workflow_run` is watched.

The adapter keeps the webhooks that it made in `github-hooks.json` in the state folder, so that it can delete them when a watch goes. It writes the file with a temp file and a rename.

"Your activity" comes from `GET /notifications` with `If-Modified-Since` and the `X-Poll-Interval` of GitHub. The adapter keeps its position in the state folder and never marks a notification as read. A fine-grained token cannot read notifications, so the status says so and the adapter stays connected.

### Sandbox

`github-cli serve --sandbox` runs the adapter against a fake GitHub in the same process. Outside Trebi it sends a fixed fake token to the fake, never a real token. The conformance check uses it:

```bash
scripts/validate.sh --conformance github
```

The skill for agents is `catalog/github/skill/SKILL.md`.
