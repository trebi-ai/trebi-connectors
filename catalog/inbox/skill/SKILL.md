---
name: inbox-cli
description: >
  Go CLI for Agentic Inbox — a self-hosted Cloudflare Workers email client.
  Use when the user wants to list, read, send, reply, forward, search, move,
  flag, draft, or manage emails on their Agentic Inbox worker (mailboxes
  hosted at a Cloudflare Worker behind Cloudflare Access). Supports
  attachments, threads, folders, and full-text search. Authenticates with
  Cloudflare Access service tokens or `cloudflared`. Triggers: "send an
  email from inbox-cli", "check my inbox", "list emails in X mailbox",
  "search emails for Y", "reply to email <id>", "download attachment".
---

# inbox-cli — Skill Reference

**What it is.** `inbox-cli` is a Go CLI wrapper around the REST API of an
[Agentic Inbox](https://github.com/cloudflare/agentic-inbox) Worker — a
self-hosted email client built on Cloudflare Workers, Durable Objects, and
R2. The Worker exposes `/api/v1/*`; the CLI gives a human and shell scripts
full coverage of that API.

**Installed?** Run `inbox-cli auth status`. Expected: `OK — base URL <url>
reachable, auth=service_token`.

## 1. Global flags (place BEFORE the subcommand)

| Flag | Meaning |
| --- | --- |
| `--mailbox <id>` | Mailbox to operate on. Falls back to `default_mailbox` in config, then to the only mailbox if exactly one exists. |
| `--base-url <url>` | One-off override of the saved base URL. |
| `-o, --output table\|json` | `table` (default) for humans; `json` for scripting. **Always use `-o json` when piping to `jq` or capturing structured output.** |
| `--verbose` | Print HTTP request/response details to stderr. (`-v` is `--version`, do not confuse them.) |
| `--no-color` | Disable color. |

`INBOX_CLI_DEBUG=1` is equivalent to `--verbose`.

Env-var overrides for config: `INBOX_CLI_BASE_URL`, `INBOX_CLI_MAILBOX`,
`INBOX_CLI_AUTH`, `CF_ACCESS_CLIENT_ID`, `CF_ACCESS_CLIENT_SECRET`,
`INBOX_CLI_CONFIG`.

## 2. Command tree

```
inbox-cli auth        login | status | logout | show
inbox-cli config      show | path
inbox-cli mailbox     list | get <id> | create | update <id> | delete <id> | use <id>
inbox-cli email       list | get <id> | send | reply <id> | forward <id> |
                      delete <id> | move <id> | archive <id> | trash <id> |
                      restore <id> | star <id> | read <id> | headers <id>
inbox-cli draft       create | update <id> | list | send <id> | delete <id>
inbox-cli thread      get <id> | read <id>
inbox-cli folder      list | create | rename <id> | delete <id>
inbox-cli search      --query <q> [filters]
inbox-cli attachment  list <email-id> | download <email-id> <attachment-id>
```

## 3. Auth (Cloudflare Access)

The Worker is gated by Cloudflare Access. Two methods, **service token is
default and what shell scripts/agents should use**:

- **Service token** (`auth_method = "service_token"`): sets
  `CF-Access-Client-Id` + `CF-Access-Client-Secret` headers. Requires a
  **Service Auth** policy on the Access app that includes the token —
  otherwise Access redirects to the IdP login page.
- **`cloudflared`**: shells out to `cloudflared access token -app <base>`
  and forwards the JWT.

To set up: `inbox-cli auth login` (interactive; prompts for base URL, auth
method, credentials, optional default mailbox). Verify with
`inbox-cli auth status` — must print `OK`.

Common auth failures and what they mean:
- `302 — Cloudflare Access redirected to its login page` → no Service Auth
  policy attached to the Access app, or token client_id/secret wrong.
- `403 Invalid or expired Access token` → `POLICY_AUD`/`TEAM_DOMAIN`
  Worker secrets are wrong.
- `expected JSON but got text/html` → almost always means Access intercepted
  the request before it reached the Worker.

When stuck, run with `--verbose` (`INBOX_CLI_DEBUG=1`) to see the request
URL, sent headers (with secrets redacted), response status, response
headers (`Content-Type`, `Cf-Ray`, `Location`, `Set-Cookie`), and a body
snippet on error.

## 4. Mailboxes

```bash
inbox-cli mailbox list                          # human table
inbox-cli -o json mailbox list                  # for scripts
inbox-cli mailbox get hello@example.com         # one mailbox + settings
inbox-cli mailbox create --email new@example.com --name "New Mailbox"
inbox-cli mailbox use me@example.com            # set default in config
inbox-cli mailbox delete old@example.com
```

Most subsequent commands accept `--mailbox <id>`. Set a default with
`mailbox use` so you don't repeat it.

## 5. Listing and reading email

```bash
# Default folder is "inbox"
inbox-cli email list                                    # 1st page, default sort=date desc
inbox-cli email list --folder sent --limit 50
inbox-cli email list --folder draft
inbox-cli email list --folder archive --threaded        # collapse by thread
inbox-cli email list --folder inbox --sort subject --desc=false

# JSON for further processing
inbox-cli -o json email list --folder inbox --limit 20 \
  | jq '.emails[] | {id, sender, subject}'

# Single message (default strips HTML to plain text)
inbox-cli email get <email-id>
inbox-cli email get <email-id> --raw                    # raw HTML body
inbox-cli email headers <email-id>                      # raw_headers JSON
```

Folders ids: `inbox`, `sent`, `draft`, `archive`, `trash`, `spam`, plus any
custom folder created via `folder create`.

## 6. Sending email

```bash
# Plain-text — single recipient
inbox-cli email send \
  --to alice@example.com \
  --subject "hello" \
  --body "Plain-text body."

# HTML body from a file + multiple recipients + attachments
inbox-cli email send \
  --to "alice@example.com,bob@example.com" \
  --cc "carol@example.com" \
  --subject "Quarterly report" \
  --html --body-file /tmp/report.html \
  --attach /tmp/report.pdf \
  --attach /tmp/spreadsheet.xlsx \
  --from-name "Hello Mailbox" \
  --reply-to some@mailbox.com

# Read body from stdin
echo "hello" | inbox-cli email send --to a@b.com --subject hi --body-file -
```

Flags:
- `--to / --cc / --bcc` — comma-separated. Validated as RFC-5322 addresses.
- `--subject` — string.
- `--body <text>` OR `--body-file <path>` (mutually exclusive). `--body-file -` reads stdin.
- `--html` — treat the body as HTML (otherwise sent as `text`).
- `--attach <path>` — repeatable; CLI base64-encodes, sniffs MIME, sets
  `Content-Disposition: attachment`.
- `--from-name <name>` — overrides the From display name (defaults to the
  mailbox itself). The From email is always the active mailbox; the API
  rejects any other From.
- `--reply-to` - Reply-To address

The Worker enforces a sender rate limit; on hit, the call returns a 429
which the CLI surfaces as an error.

## 7. Reply / forward

These hit dedicated Worker endpoints that rebuild RFC-5322 threading
headers (`In-Reply-To`, `References`) and prepend a quoted reply block
server-side — the CLI does not duplicate that logic.

```bash
# Reply — to/subject default to the original's sender + "Re: ..."
inbox-cli email reply <email-id> --body "Thanks, will do."

# Reply, overriding subject / adding cc
inbox-cli email reply <email-id> \
  --cc team@example.com \
  --subject "Re: revised plan" \
  --html --body-file /tmp/reply.html

# Forward — subject auto-prefixes "Fwd: ..." if missing
inbox-cli email forward <email-id> \
  --to recipient@example.com \
  --body "FYI"
```

## 8. Drafts

```bash
inbox-cli draft create --to alice@b.com --subject hi --body "wip"
inbox-cli draft list
inbox-cli draft update <draft-id> --body "fresh wip"   # replaces draft
inbox-cli draft send <draft-id>                        # promotes & deletes
inbox-cli draft delete <draft-id>
```

`draft send` reads the draft, then routes:
- if the draft has `in_reply_to`, it uses the `/reply` endpoint so threading
  is reconstructed canonically;
- otherwise it uses the regular send endpoint.
The original draft is then deleted.

## 9. Threads

```bash
inbox-cli thread get <thread-id>           # all messages, oldest first
inbox-cli thread get <thread-id> --text    # strip HTML to text
inbox-cli thread read <thread-id>          # mark every message read
```

Get a thread id from `email get <id>` (the `Thread` field) or from a
threaded `email list --threaded`.

## 10. Folders

```bash
inbox-cli folder list
inbox-cli folder create --name "Receipts"      # id is slugified from name
inbox-cli folder rename <folder-id> --name "Bills"
inbox-cli folder delete <folder-id>            # only custom folders
```

System folders (`inbox`, `sent`, `draft`, `archive`, `trash`, `spam`) cannot
be deleted.

## 11. Move / archive / trash / restore / star / read

```bash
inbox-cli email move <id> --folder receipts
inbox-cli email archive <id>      # convenience for --folder archive
inbox-cli email trash <id>        # convenience for --folder trash
inbox-cli email restore <id>      # convenience for --folder inbox
inbox-cli email delete <id>       # permanent, also drops attachments

inbox-cli email star <id>            # star
inbox-cli email star <id> --unstar   # unstar
inbox-cli email read <id>            # mark read
inbox-cli email read <id> --unread   # mark unread
```

## 12. Search

Hits the Worker's full-text search endpoint with optional filters.

```bash
# Basic
inbox-cli search --query "invoice"

# Filtered
inbox-cli search \
  --query "report" \
  --folder inbox \
  --from boss@company.com \
  --has-attachment true \
  --read false \
  --date-start 2026-04-01 \
  --date-end 2026-05-01 \
  --limit 50

# JSON for scripting
inbox-cli -o json search --query "stripe receipt" \
  | jq -r '.emails[] | "\(.date)  \(.sender)  \(.subject)"'
```

Tri-state filter flags accept `true|false|1|0|yes|no`; omit to leave
unfiltered. Available: `--read`, `--starred`, `--has-attachment`. Other
filters take strings: `--folder`, `--from`, `--to`, `--subject`,
`--date-start`, `--date-end`. Pagination: `--page`, `--limit`.

## 13. Attachments

```bash
# List attachments on an email
inbox-cli attachment list <email-id>

# Download (saves to original filename in cwd by default)
inbox-cli attachment download <email-id> <attachment-id>
inbox-cli attachment download <email-id> <attachment-id> --out /tmp/foo.pdf
inbox-cli attachment download <email-id> <attachment-id> --out -    # to stdout

# Bulk download
for id in $(inbox-cli -o json attachment list <email-id> | jq -r '.[].id'); do
  inbox-cli attachment download <email-id> "$id"
done
```

## 14. Output conventions

- **Default `table`**: aligned columns for lists; labeled key/value blocks
  for single-record output. Truncates long fields with `…`.
- **`-o json`**: prints the raw API JSON, indented. **Use this whenever the
  output will be parsed by another tool.** Lists are typically
  `{ "emails": [...], "totalCount": N }`.

## 15. Common workflows for an agent

**Triage inbox unread.**
```bash
inbox-cli -o json search --query "" --folder inbox --read false --limit 50 \
  | jq -r '.emails[] | "\(.id)\t\(.sender)\t\(.subject)"'
```

**Read latest message and reply.**
```bash
ID=$(inbox-cli -o json email list --folder inbox --limit 1 | jq -r '.emails[0].id')
inbox-cli email get "$ID" --raw
inbox-cli email reply "$ID" --body "Got it, thanks."
```

**Save all attachments from a thread.**
```bash
THREAD=<thread-id>
mkdir -p ./out
for eid in $(inbox-cli -o json thread get "$THREAD" | jq -r '.[].id'); do
  for aid in $(inbox-cli -o json attachment list "$eid" | jq -r '.[].id'); do
    inbox-cli attachment download "$eid" "$aid" --out "./out/${eid}_${aid}"
  done
done
```

**Send a templated HTML notification.**
```bash
cat > /tmp/notify.html <<'HTML'
<p>Build <strong>$BUILD_ID</strong> finished: <code>$STATUS</code></p>
HTML
inbox-cli email send \
  --to ops@example.com \
  --subject "Build $STATUS" \
  --html --body-file /tmp/notify.html
```

## 16. Important behaviors and gotchas

- **`-v` is `--version`, not verbose.** Use `--verbose` (or
  `INBOX_CLI_DEBUG=1`) for HTTP tracing.
- **Mailbox resolution order**: `--mailbox` flag → `default_mailbox` from
  config → the single existing mailbox (if exactly one) → error.
- **Recipients are validated locally** before the call (RFC 5322).
- **From address is fixed to the mailbox** by the Worker; you can only
  customize `--from-name`. The CLI never asks for a From email.
- **Reply/forward DO NOT take `--to` as a requirement for reply**: if
  omitted, the CLI fetches the original and uses its sender. For forward,
  `--to` is required.
- **Attachments**: `--attach <path>` (the `@` prefix is also accepted, e.g.
  `--attach @/tmp/file.pdf`). MIME type is sniffed via file extension.
- **Drafts store HTML** by convention; `draft send` uses the body as HTML.
- **Custom folder ids are slugified from the name**: spaces become dashes,
  punctuation is stripped. Use `folder list` to discover the id.
- **Deleted attachments are gone from R2** after `email delete`.
- **No `mailbox-cli` command for spam-folder listing exists separately** —
  use `email list --folder spam`.

## 17. Where things live

- Build script: `inbox-cli/scripts/build.sh` → `inbox-cli/build/inbox-cli`
- Config file: `~/.cli-tools/inbox-cli/config.toml` (chmod 600)
- Worker REST source for the API surface: `workers/index.ts`
- Schemas mirrored in Go: `internal/client/types.go`

When in doubt, `inbox-cli <command> --help` prints exact flags.
