---
name: github-cli
description: >
  GitHub CLI tool. Use when the user wants to list GitHub repositories, read the recent events or the webhooks of a repository, or start work when something happens in a GitHub repository.
  Triggers: "github repos", "github events", "watch a github repository", "when a pull request opens".
---

# github-cli — GitHub CLI

Go CLI for GitHub: repositories, events, and webhooks. In Trebi it also sends events from the watched repositories. Binary: `github-cli`.

## Auth

In Trebi, click "Log in" on the connection and type the code on the GitHub page. Or set the connection input "Personal access token" (`GITHUB_TOKEN`) with a fine-grained token that has "Webhooks: read and write" and "Contents: read". The token input wins over the login. The `--token` flag fails with "Trebi sets this value".

On its own, the order is: the `--token` flag, the `GITHUB_TOKEN` env, the `GH_TOKEN` env, then `~/.cli-tools/github-cli/config.json` (`{"token": "ghp_…"}`).

## Commands

Add `--json` (or `-j`) before the command for JSON output.

| Command | What it does |
|---|---|
| `github-cli repos list [--limit 30]` | List the repositories of the account, newest push first, with the admin access. |
| `github-cli events list <owner/repo>` | List the recent events of a repository. |
| `github-cli hooks list <owner/repo>` | List the webhooks of a repository. You must be an admin of it. |
| `github-cli serve [--sandbox]` | Serve the `trebi-connector/1` protocol for Trebi. `--sandbox` uses a fake GitHub in the same process. |
| `github-cli --version` | Show the version. |

## Events

The room of each event is the repository, for example `octo/app`. The thread of an issue or a pull request event is its number. `sender.id` is the GitHub login, and `sender.bot` is true for a bot account.

- `push`: commits pushed to a branch. `data` has `branch`, `ref`, `head`, `url`, and `commits`.
- `pull_request`: a pull request opened, changed, or closed. `data` has `action`, `number`, `title`, `state`, `url`, `merged`, `draft`, `author`, `base`, and `head`.
- `pull_request_review`: a review on a pull request. `data` has `action`, `number`, `title`, `state`, `url`, `author`, and `body`.
- `issues`: an issue opened, changed, or closed. `data` has `action`, `number`, `title`, `state`, `url`, `author`, and `labels`.
- `issue_comment`: a comment on an issue or a pull request. `data` has `action`, `number`, `title`, `url`, `author`, `body`, and `is_pull_request`.
- `release`: a release published or changed. `data` has `action`, `tag`, `name`, `url`, and `prerelease`.
- `workflow_run`: an Actions run that starts or ends. `data` has `action`, `run_id`, `name`, `status`, `conclusion`, `branch`, and `url`.

Every `data` also has `repository`. `raw` has the full GitHub payload when it is smaller than 64 KiB.

To get events, watch a repository in the connection. With Trebi Cloud, Trebi adds a webhook to the repository, and GitHub sends each change at once. You must be an admin of the repository. Without Trebi Cloud, Trebi reads the repository events every minute or more. This mode has no `workflow_run` events.

A switch between the live mode and the check every minute can send one change two times with a different event id. A webhook event id is the GitHub delivery id; a checked event id starts with `poll:`. A job that must act one time for each change can check the pull request or issue number and its `updated_at` in `raw`.

## Examples

```bash
github-cli repos list --limit 10
github-cli -j events list octo/app
github-cli hooks list octo/app
```
