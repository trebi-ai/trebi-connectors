# github-cli

`github-cli` is a small command line tool for GitHub. It lists repositories, the recent events of a repository, and its webhooks. Its `serve` command speaks the `trebi-connector/1` protocol, so Trebi can start work when something happens in a repository.

## Use on its own

Build it with `scripts/build.sh [version] [output]`, or with `go build .`.

Give the tool a GitHub token. The tool reads the token in this order: the `--token` flag, the `GITHUB_TOKEN` env, the `GH_TOKEN` env, then `~/.cli-tools/github-cli/config.json` with the content `{"token": "ghp_…"}`.

```bash
github-cli repos list
github-cli --json events list octo/app
github-cli hooks list octo/app
github-cli --version
```

## Use with Trebi

Add the GitHub connector from the catalog. Click "Log in" and type the code on the GitHub page, or set the "Personal access token" input. Trebi runs `github-cli serve` with `TREBI_STATE_DIR`. In this mode the tool reads the token from `GITHUB_TOKEN`, then from `auth.json` in the state folder, which the login writes. The `--token` flag fails with "Trebi sets this value".

The login uses the GitHub device flow with the Trebi OAuth app and the scopes `repo` and `admin:repo_hook`. The build sets the app id with `TREBI_GITHUB_CLIENT_ID`. A build without it needs the token input.

Watch a repository in the connection to get events. With Trebi Cloud, the adapter adds one webhook to each watched repository with the hosted URL and secret, and checks the `x-hub-signature-256` header of each delivery. You must be an admin of the repository. Without Trebi Cloud, the adapter reads the repository events every minute or more, with `If-None-Match`.

The adapter keeps the webhooks that it made in `github-hooks.json` in the state folder, so that it can delete them when a watch goes. It writes the file with a temp file and a rename.

`github-cli serve --sandbox` runs the adapter against a fake GitHub in the same process. Outside Trebi it sends a fixed fake token to the fake, never a real token. The conformance check uses it:

```bash
scripts/validate.sh --conformance github
```

The skill for agents is `catalog/github/skill/SKILL.md`.
