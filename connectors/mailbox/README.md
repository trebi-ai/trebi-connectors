# mailbox

A small Go CLI that reads and writes one mail account over IMAP and SMTP. It searches and reads mail and conversations, saves drafts and replies, sends mail, and sorts the inbox. Its `watch` command prints one event for each new message. Its `serve` command is the Trebi adapter that sends the same events and reports the health of the account. The catalog entry `catalog/gmail` uses it.

## Build

```bash
go build -o mailbox .
./scripts/build.sh            # writes ./build/mailbox with version metadata
```

## Use on its own

With no `TREBI_STATE_DIR` in the env, `mailbox` is a normal CLI. It reads these env names:

| Env | What it is |
|---|---|
| `MAILBOX_ADDRESS` | The address. It is also the IMAP and SMTP user name. |
| `MAILBOX_PASSWORD` | The password. For Gmail and iCloud, use an app password. |
| `MAILBOX_PROVIDER` | `gmail`, `icloud`, `fastmail`, or `outlook`. The `--provider` flag wins over it. |
| `MAILBOX_IMAP_HOST` | `host:port`. Port 143 uses STARTTLS; another port uses TLS. The default port is 993. |
| `MAILBOX_SMTP_HOST` | `host:port`. Port 465 uses TLS; another port must offer STARTTLS. The default port is 587. |
| `MAILBOX_DRAFTS` | The Drafts folder. With no value, the program finds the folder with the `\Drafts` attribute. |
| `MAILBOX_SENT` | The folder for the sent copy. An empty value saves no copy. Gmail saves the copy itself. |

A host or folder env wins over the provider preset. The program never sends the password over a connection with no TLS.

Each operation reads a JSON object on stdin and writes a JSON object on stdout:

```bash
echo '{"query": "invoice", "limit": 5}' | mailbox --provider gmail op search
echo '{"id": "INBOX:42"}' | mailbox --provider gmail op read
echo '{"to": ["ann@example.com"], "subject": "Re: Lunch", "text": "Yes.", "in_reply_to": "<lunch-1@example.com>"}' | mailbox --provider gmail op draft
echo '{"to": ["ann@example.com"], "subject": "Hello", "text": "Hi Ann"}' | mailbox --provider gmail op send
```

- `search` takes `query`, `from`, `to`, `since` and `before` (`YYYY-MM-DD`), `unread`, `folder` (default `INBOX`), `limit` (default 20, max 100), and `cursor`. It returns `messages`, newest first, and `next_cursor`. Give `next_cursor` back as `cursor` to get the next page. An empty `next_cursor` is the last page. An `id` is `<folder>:<uid>`. On Gmail, `search` also takes `gmail_query` (the Gmail search syntax; the default folder is then `[Gmail]/All Mail`), and an `id` is `gm:<X-GM-MSGID>`, which stays the same when the labels change. Gmail results also have `thread_id` (the X-GM-THRID) and `labels`.
- `read` takes `id` and `html`. It returns `message` with `headers`, `text`, and `attachments`. The text is the plain part, or the HTML part as text. It never returns attachment bytes. `html: true` also returns the HTML part.
- `attachment` takes `id` and `name` or `index` (1 is the first attachment of `read`). It saves the file in `attachments/<id>/` in the state folder (`~/.cli-tools/mailbox` outside Trebi) and returns `path`, `name`, `type`, and `size`.
- `thread` takes `id`, `limit` (default 20, max 50), and `html`. It returns `thread_id` and `messages` of the conversation, oldest first, from the folder of the message and the Sent folder. It removes the quoted text of earlier messages.
- `draft` and `send` take `to`, `cc`, `bcc`, `subject`, `text`, `html`, `attachments`, and `in_reply_to`, or one `mime_base64` with a full MIME message. An attachment is the full path of a file as a string, `{"path"}`, or `{"name", "content_base64"}`, with an optional `name` and `mime`. Trebi sends only the string form; the whole message is at most 25 MiB. With only `html`, the program makes the text part. `draft` saves the message in the Drafts folder with the `\Draft` flag. `send` sends it over SMTP and then saves a copy in the Sent folder, when there is one.
- `reply` and `reply_send` take `id`, `text` or `html`, `all`, `cc`, `bcc`, and `attachments`. The program sets the recipients (Reply-To wins over From; `all` adds the other recipients but not this account), the subject, `In-Reply-To`, and `References`. `reply` saves a draft. `reply_send` sends.
- `send_draft` takes the `id` of a draft. It sends the draft as saved, then deletes it. `draft_delete` deletes a draft. Both refuse a message that is not a draft in the Drafts folder.
- `mark` takes `ids` (at most 100), `read`, and `starred`. `label` takes `ids`, `add`, `remove`, and `create`. `archive` and `trash` take `ids`. Each returns `changed` and `failed` (an `id` and a `reason`). On Gmail, `label` and `archive` change the Gmail labels (`archive` removes `Inbox`), and `thread` reads the Gmail thread from All Mail. On a server with no labels, `label` copies to the folder for `add` and refuses `remove`.
- `folders` returns each folder with its `role` (inbox, drafts, sent, trash, archive, junk, all), `total`, and `unread`.
- Search, read, and thread do not mark a message as read. An unknown input field is an error.

### Watch for new mail

`mailbox watch` prints one JSON line on stdout for each new message in INBOX. Each line is a `mail.received` event, the same as `serve` sends. The cursor is in `~/.cli-tools/mailbox/cursor.json`.

- The first start sends no events for old mail. It saves the newest message as the start point.
- After a restart, it sends each message that came in while it was stopped, in order, once.
- When the server makes the folder again (a new UIDVALIDITY), it saves a new start point and logs a warning.
- Mail from the address of the account sends no event.

## Use with Trebi

Trebi runs `mailbox --provider gmail serve` with `TREBI_STATE_DIR` set. This is Trebi mode. The values come only from the inputs of the connection. The program writes only to the state folder: the cursor of the events and the saved attachments.

- With no address or no password, `serve` sends `status` `auth_required` with reason `missing_input`.
- `serve` logs in at start, then waits in IDLE and sends a `mail.received` event for each new message in INBOX. A catch-up sync runs after each wake-up, each reconnect, and every 5 minutes. The cursor is `cursor.json` in the state folder. A cursor from a newer version gives `error` and stays on disk.
- A refused password gives `auth_required` with reason `revoked` and a message that says what to do. A network fault gives `connecting`, with a backoff of 1 s to 2 min. After 10 faults in sequence, the status is `error`.
- The daemon runs each operation as `mailbox --provider gmail op <name>` with the inputs in the env. A run never gets the program or the password.
- `serve --sandbox` runs the same adapter over a fake IMAP and SMTP server in the same process. Outside Trebi it uses fixed fake credentials.

The rules for all connector programs are in "Adapter folder contract" in `../../CLAUDE.md`.
