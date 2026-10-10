# mailbox

A small Go CLI that reads and writes one mail account over IMAP and SMTP. It searches and reads mail, saves drafts, and sends mail. Its `serve` command is the Trebi adapter that reports the health of the account. The catalog entry `catalog/gmail` uses it.

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

- `search` takes `query`, `from`, `since` (`YYYY-MM-DD`), `unread`, `folder` (default `INBOX`), and `limit` (default 20, max 100). It returns `messages`, newest first. An `id` is `<folder>:<uid>`.
- `read` takes `id` and `html`. It returns `message` with `headers`, `text`, and `attachments`. The text is the plain part, or the HTML part as text. It never returns attachment bytes. `html: true` also returns the HTML part.
- `draft` and `send` take `to`, `cc`, `bcc`, `subject`, `text`, and `in_reply_to`, or one `mime_base64` with a full MIME message. `draft` saves the message in the Drafts folder with the `\Draft` flag. `send` sends it over SMTP and then saves a copy in the Sent folder, when there is one.
- Search and read do not mark a message as read. An unknown input field is an error.

## Use with Trebi

Trebi runs `mailbox --provider gmail serve` with `TREBI_STATE_DIR` set. This is Trebi mode. The values come only from the inputs of the connection. The program writes no file.

- With no address or no password, `serve` sends `status` `auth_required` with reason `missing_input`.
- `serve` logs in at start and every 5 minutes. A refused password gives `auth_required` with reason `revoked`. A network fault gives `error`.
- The daemon runs each operation as `mailbox --provider gmail op <name>` with the inputs in the env. A run never gets the program or the password.
- `serve --sandbox` runs the same adapter over a fake IMAP and SMTP server in the same process. Outside Trebi it uses fixed fake credentials.

The rules for all connector programs are in "Adapter folder contract" in `../../CLAUDE.md`.
