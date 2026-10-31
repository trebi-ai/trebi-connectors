---
name: github-cli
description: >
  GitHub in Trebi: the gh command with the login of the connection, operations to find, read, comment on, review, and merge issues and pull requests, and events from watched repositories and from your own GitHub activity. Use when the user wants to do GitHub work in a job, read or change issues and pull requests, run a workflow again, or start work when something happens in a repository or needs them.
  Triggers: "github", "gh", "pull request", "review my PRs", "what needs me on github", "github issues", "watch a github repository", "when a pull request opens", "when CI fails", "when someone mentions me on github".
---

# GitHub in Trebi

The GitHub connector gives three things:

- `gh`, the official GitHub command, with the login of the connection. Use it for most work.
- Operations that the assistant can call: three that only read, and five that change data and need approval.
- Events: changes in the repositories that the connection watches, and "your activity" (review requests, mentions, assignments, and more).

Binaries: `gh` and `github-cli`. Source: `connectors/github-cli` in `github.com/trebi-ai/trebi-connectors`.

## Use gh first

In a run, `gh` is the gh of the connection. It always uses the login of the connection, not the personal gh login of the user, and it works for github.com only. Prompts, colors, and the pager are off. `git clone`, `git fetch`, and `git push` of a `https://github.com/...` remote also use the login of the connection.

Use `--json` with a field list for output that you parse, and `gh api` for each endpoint that has no gh command:

```bash
gh pr list -R octo/app --json number,title,author,reviewDecision,statusCheckRollup
gh pr view 42 -R octo/app --json title,body,files,reviews,comments
gh pr diff 42 -R octo/app
gh pr checks 42 -R octo/app
gh issue list -R octo/app --label bug --json number,title,assignees
gh run list -R octo/app --status failure --limit 5 --json databaseId,name,headBranch,url
gh run view 123456 -R octo/app --log-failed
gh search prs --review-requested=@me --state=open --json repository,number,title,url
gh api repos/octo/app/actions/runs/123456/jobs --jq '.jobs[] | {name, conclusion}'
gh repo clone octo/app
```

These commands change data. Do them only when the job or the user asks for that change:

- `gh pr merge`, `gh pr close`, `gh issue close`
- `gh release create`, `gh release delete`
- `gh repo delete`, `gh repo archive`
- `gh workflow run`
- `gh api -X POST`, `gh api -X PATCH`, `gh api -X PUT`, `gh api -X DELETE`, and `gh api` with `-f` or `-F` fields (these send a POST)

Trebi refuses these gh commands in a run, and gh stops with exit code 2:

- `gh auth login`, `gh auth logout`, `gh auth refresh`, `gh auth switch`, `gh auth setup-git`, `gh auth token`. The connection holds the login. `gh auth status` works.
- `gh extension install`, `gh extension upgrade`
- `gh codespace`

When the connection has no login, gh stops with exit code 4 and the message "GitHub is not logged in. Log in on the GitHub connection in Trebi." Tell the user to log in on the GitHub connection.

## github-cli commands

Add `--json` (or `-j`) before the command for JSON output.

| Command | What it does |
|---|---|
| `github-cli whoami` | Show the account of the login, its name, and its scopes. |
| `github-cli repos list [--limit 30]` | List the repositories of the account, newest push first, with the admin access. |
| `github-cli events list <owner/repo>` | List the recent events of a repository. |
| `github-cli hooks list <owner/repo>` | List the webhooks of a repository. You must be an admin of it. |
| `github-cli op <name>` | Run one operation with JSON on stdin (see "Operations"). |
| `github-cli serve [--sandbox]` | Serve the `trebi-connector/1` protocol for Trebi. `--sandbox` uses a fake GitHub in the same process. |
| `github-cli --version` | Show the version. |

## Operations

The assistant and jobs can call these operations of the connection. Each one takes a JSON object and gives a JSON object. An error gives `{"error": "...", "status": <GitHub status>}`. Exit code 2 is a bad input, and exit code 1 is a GitHub error.

These three only read. They are on by default:

| Operation | What it does | Input |
|---|---|---|
| `search` | Find issues and pull requests with the GitHub search words. The most recently changed comes first. | `query` (required), `limit` (1 to 50, default 20) |
| `get` | Read one issue or pull request with its last comments. A pull request also has its branches, reviews, and checks. A long body is cut at 8 KiB and has `truncated: true`. | `repository`, `number` (both required), `comments` (0 to 50, default 10) |
| `my_work` | List `review_requested`, `assigned`, and `my_pull_requests`. Each of your pull requests has `checks` and `review_decision`. | `limit` (1 to 50 for each list, default 20) |

These five change data. Trebi asks the user before each call:

| Operation | What it does | Input |
|---|---|---|
| `comment` | Add a comment to an issue or a pull request. | `repository`, `number`, `body` (all required) |
| `create_issue` | Open an issue. | `repository`, `title` (required), `body`, `labels`, `assignees` |
| `review_pr` | Approve, ask for changes, or comment. | `repository`, `number`, `verdict` (`approve`, `request_changes`, or `comment`; all required), `body` (required for `request_changes` and `comment`) |
| `merge_pr` | Merge a pull request. | `repository`, `number` (required), `method` (`merge`, `squash`, or `rebase`; default `squash`), `delete_branch` (default false) |
| `rerun_workflow` | Run a workflow run again. | `repository`, `run_id` (required), `failed_only` (default true) |

`merge_pr` with `delete_branch` deletes only a branch of the same repository that is not its main branch. GitHub refuses an approval of your own pull request.

## Login

The connection has three login methods:

1. "Use the GitHub login on this machine": Trebi asks gh on this machine for its token each time it needs one. It does not copy the token. When the user logs out of gh, the connection asks for a login again. Log out of the connection does not log out of gh.
2. "Sign in with GitHub": the user types a code on a GitHub page. Trebi keeps the token in the state folder of the connection. It asks for the scopes `repo`, `read:org`, and `admin:repo_hook`.
3. "Paste a token": the input "Personal access token" (`GITHUB_TOKEN`). Trebi tests it with `github-cli whoami`. A classic token needs the scopes `repo`, `read:org`, and `admin:repo_hook`. A fine-grained token also works, but it gives no "your activity" events.

The token input wins over the other two. The `--token` flag fails with "Trebi sets this value".

An organization can limit the access of OAuth apps. When a repository of an organization is missing or gives 404 after "Sign in with GitHub", the organization did not approve the Trebi app. The user can ask for access on GitHub (Settings, Applications, Authorized OAuth Apps, Trebi), and an owner approves it in the organization settings (Third-party access). Or use another login method.

On its own, outside Trebi, the order is: the `--token` flag, the `GITHUB_TOKEN` env, the `GH_TOKEN` env, then `~/.cli-tools/github-cli/config.json` (`{"token": "ghp_…"}`). Outside Trebi, `gh` of this connector is the normal gh with no change.

## Events

The room of each event is the repository, for example `octo/app`. The thread of an issue, a pull request, or a discussion event is its number. `sender.id` is the GitHub login, `sender.bot` is true for a bot account, and `sender.self` is true for the account of the login. Every `data` has `repository`. Most also have `action`, `url`, and `author`. `raw` has the full GitHub payload when it is smaller than 64 KiB.

### Repository events

Watch a repository in the connection to get these events. Each watch has a mode:

- Live: Trebi adds a webhook to the repository, and GitHub sends each change at once. This mode needs Trebi Cloud and a login that is an admin of the repository and can add webhooks.
- Check every minute: Trebi reads the repository events every minute or more. This mode is for a repository that you can only read, a login that cannot add webhooks, or no Trebi Cloud. The watch shows why it uses this mode.

| Type | When | `data` also has |
|---|---|---|
| `push` | Commits pushed to a branch | `branch`, `ref`, `head`, `commits` |
| `pull_request` | A pull request opened, changed, or closed | `number`, `title`, `state`, `merged`, `draft`, `base`, `head` |
| `pull_request_review` | A review on a pull request | `number`, `title`, `state`, `body` |
| `pull_request_review_comment` | A comment on a line of a pull request | `number`, `title`, `body`, `path`, `line` |
| `issues` | An issue opened, changed, or closed | `number`, `title`, `state`, `labels` |
| `issue_comment` | A comment on an issue or a pull request | `number`, `title`, `body`, `is_pull_request` |
| `release` | A release published or changed | `tag`, `name`, `prerelease` |
| `workflow_run` | An Actions workflow run that starts or ends | `run_id`, `name`, `status`, `conclusion`, `branch` |
| `create` | A branch or a tag created | `ref`, `ref_type` |
| `delete` | A branch or a tag deleted | `ref`, `ref_type` |
| `check_suite` | Checks on a commit end, for example CI from another service | `suite_id`, `app`, `status`, `conclusion`, `branch`, `head`, `number` |
| `status` | A commit status from another service | `sha`, `state`, `context`, `description`, `branches` |
| `deployment_status` | A deployment that starts, succeeds, or fails | `deployment_id`, `environment`, `state`, `description`, `ref`, `sha` |
| `discussion` | A discussion opened, changed, or answered | `number`, `title`, `category`, `state`, `answer_url` |
| `discussion_comment` | A comment on a discussion | `number`, `title`, `category`, `body` |

`check_suite`, `status`, `deployment_status`, `discussion`, and `discussion_comment` come only in the live mode. A watch that checks every minute never sends them. In that mode, `workflow_run` comes from a check of the Actions runs.

A switch between the two modes can send one change two times with a different event id. A webhook event id is the GitHub delivery id, and a checked event id starts with `poll:`. A job that must act one time for each change can check the number and the `updated_at` in `raw`.

### Your activity

These events come from the GitHub notifications of the login, for all repositories. They need no watch. Trebi checks them every minute or more, as GitHub asks.

| Type | When |
|---|---|
| `review_requested` | Someone asks for your review on a pull request |
| `mention` | Someone mentions you or your team (`data.reason` is `mention` or `team_mention`) |
| `assigned` | Someone assigns an issue or a pull request to you |
| `ci_activity` | A workflow run that you started ends |
| `activity` | Other activity on a thread you follow (`data.reason` tells why) |

`data` has `reason`, `subject_type`, `title`, `url`, `number` (when the thread has one), `thread_id`, and `updated_at`. The sender is the author of the newest comment, or else the author of the issue or the pull request. Trebi does not mark the notifications as read.

A fine-grained token cannot read notifications. The connection then stays connected with the message "Your activity needs the GitHub login, not a fine-grained token.", and it sends no "your activity" events.

## Examples

```bash
github-cli whoami
github-cli repos list --limit 10
echo '{"query":"repo:octo/app is:pr is:open review-requested:@me"}' | github-cli op search
echo '{"repository":"octo/app","number":42}' | github-cli op get
echo '{}' | github-cli op my_work
```
