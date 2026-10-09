---
name: mstodo-cli
description: >
  Microsoft To Do CLI. Use when the user wants to read or change To Do lists, tasks, steps, links, files, or extensions, to watch a list for changes, or when a job starts from a task event of the Microsoft To Do connector.
  Triggers: "microsoft to do", "to do list", "todo tasks", "add a task to to do", "complete a to do task", "watch a to do list", "task changed in to do".
---

# mstodo-cli — Microsoft To Do CLI

Go CLI for Microsoft To Do through Microsoft Graph. Binary: `mstodo-cli`.

The login asks for the scopes `openid profile offline_access Tasks.ReadWrite`. The CLI cannot read mail, files, or the calendar of the account.

## Rules

- Each command takes named flags. Do not give positional arguments.
- `--list` (`-l`) takes a list id or a list name. A name is not case sensitive. When two lists have the same name, use the id.
- `--task` (`-t`) takes a task id. `--id` takes the id of a step, a link, a file, or a subscription.
- Add `--json` (`-j`) to any command for JSON output. Use JSON when you read ids or fields.
- These commands change data: each `create`, `update`, `delete`, `complete`, `reopen`, `check`, `uncheck`, `add`, and `renew`. Ask the user before you delete a list, a task, or a file.

## Login

```bash
mstodo-cli auth status --json   # logged_in, valid, account
```

If the login is missing or not valid, tell the user to log in again from the Trebi connection.

## Lists

```bash
mstodo-cli lists list                         # id, name, kind (defaultList, flaggedEmails)
mstodo-cli lists get --list Groceries
mstodo-cli lists create --name "Trip"
mstodo-cli lists update --list Trip --name "Trip 2026"
mstodo-cli lists delete --list "Trip 2026"     # also deletes its tasks
mstodo-cli lists delta                        # all lists and a delta_link
mstodo-cli lists delta --link '<delta_link>'  # only the changes since that link
```

## Tasks

```bash
mstodo-cli tasks list --list Groceries                    # ID  STATUS  IMPORTANCE  DUE  TITLE
mstodo-cli tasks list --list Groceries --status open      # not completed
mstodo-cli tasks list --list Work --filter "importance eq 'high'" --orderby "dueDateTime/dateTime" --limit 10
mstodo-cli tasks get --list Work --task <task id> --json
mstodo-cli tasks create --list Work --title "Send the report" --due 2026-10-20 --importance high
mstodo-cli tasks create --list Work --title "Call Ana" --reminder 2026-10-19T09:00 --tz "America/Sao_Paulo" --note "about the contract"
mstodo-cli tasks update --list Work --task <task id> --title "Send the final report" --due none
mstodo-cli tasks complete --list Work --task <task id>
mstodo-cli tasks reopen --list Work --task <task id>
mstodo-cli tasks delete --list Work --task <task id>
mstodo-cli tasks delta --list Work [--link '<delta_link>']
```

Graph sorts tasks only on some fields, for example `createdDateTime` and `dueDateTime/dateTime`. `--orderby title` fails with 400. Graph keeps the dates in UTC, so `get` can show a date in UTC that you set in another time zone.

Field flags of `create` and `update`:

- `--title`, `--note` (text), `--note-html`.
- `--due` and `--start` take `YYYY-MM-DD` or `YYYY-MM-DDTHH:MM`. `none` removes the date.
- `--reminder YYYY-MM-DDTHH:MM` turns the reminder on. `--no-reminder` turns it off.
- `--tz` is the time zone of the dates. The default is `UTC`.
- `--importance` is `low`, `normal`, or `high`.
- `--status` is `notStarted`, `inProgress`, `completed`, `waitingOnOthers`, or `deferred`.
- `--category` adds one category. Repeat the flag for more. On `update`, the flags replace all categories.
- `--recurrence` takes a Graph `patternedRecurrence` as JSON. `none` removes it. Example: `'{"pattern":{"type":"weekly","interval":1,"daysOfWeek":["monday"]},"range":{"type":"noEnd","startDate":"2026-10-12"}}'`.
- `--data` takes more `todoTask` fields as JSON, or `@file`. A named flag wins over the same field in `--data`.

## Steps (checklist items)

```bash
mstodo-cli checklist list   --list Trip --task <task id>
mstodo-cli checklist create --list Trip --task <task id> --name "Passport"
mstodo-cli checklist check  --list Trip --task <task id> --id <step id>
mstodo-cli checklist uncheck --list Trip --task <task id> --id <step id>
mstodo-cli checklist update --list Trip --task <task id> --id <step id> --name "Passport and visa"
mstodo-cli checklist delete --list Trip --task <task id> --id <step id>
```

## Links (linked resources)

A link points from a task to an item in another app.

```bash
mstodo-cli links list   --list Work --task <task id>
mstodo-cli links create --list Work --task <task id> --app "GitHub" --name "Issue 42" --url "https://github.com/o/r/issues/42" [--external-id 42]
mstodo-cli links update --list Work --task <task id> --id <link id> --name "Issue 42 (closed)"
mstodo-cli links delete --list Work --task <task id> --id <link id>
```

## Files (attachments)

A file can have at most 25 MB. The CLI sends a file of 3 MB or more in parts.

```bash
mstodo-cli attachments list --list Work --task <task id>                 # ID  NAME  TYPE  SIZE
mstodo-cli attachments add  --list Work --task <task id> --file ./report.pdf [--name "Report.pdf"]
mstodo-cli attachments get  --list Work --task <task id> --id <file id> -o ./report.pdf   # -o - writes to stdout
mstodo-cli attachments delete --list Work --task <task id> --id <file id>
```

## Extensions (custom data)

An open extension keeps your own JSON fields on a list, or on a task with `--task`. For some accounts, for example personal Microsoft accounts, Microsoft does not list extensions: `extensions list` fails with 404. Then use `extensions get --name`.

```bash
mstodo-cli extensions create --list Work [--task <task id>] --name com.example.sync --data '{"externalId":"A-1"}'
mstodo-cli extensions get    --list Work [--task <task id>] --name com.example.sync
mstodo-cli extensions update --list Work [--task <task id>] --name com.example.sync --data '{"externalId":"A-2"}'
mstodo-cli extensions list   --list Work [--task <task id>]
mstodo-cli extensions delete --list Work [--task <task id>] --name com.example.sync
```

## Watch changes from the CLI

`watch` asks Microsoft for the changes of the lists, at each interval. It writes one JSON line for each change, until it stops. It needs no public URL.

```bash
mstodo-cli watch --list Work --list Groceries --interval 30s [--from-start]
# {"change":"created","list":"Work","list_id":"...","task":{...},"at":"2026-10-09T12:00:00Z"}
```

`change` is `created`, `updated`, or `deleted`. A deleted task has only its `id` and `@removed`. When a task is created and deleted between two polls, Microsoft can send no change for it.

## Graph subscriptions (webhooks)

A subscription makes Microsoft post each task change of a list to a public HTTPS URL. The URL must send back the `validationToken` query value as `text/plain` with status 200 in 10 seconds. A subscription ends after at most 70h30m. Renew it before then.

```bash
mstodo-cli subscriptions create --list Work --url https://example.com/hook [--change-type created,updated,deleted] [--expires 70h] [--client-state <secret>]
mstodo-cli subscriptions list
mstodo-cli subscriptions get   --id <subscription id>
mstodo-cli subscriptions renew --id <subscription id> [--expires 70h]
mstodo-cli subscriptions delete --id <subscription id>
```

The Trebi connector makes and renews its own subscriptions. Do not delete a subscription that you did not make.

## Events

The connector sends one event type, `task`. The room is the list. The data has:

- `change`: `created`, `updated`, or `deleted`.
- `task_id` and `list_id`.
- `title`, `status`, `importance`, `categories`, `note`, `due`, `start`, `reminder`, `has_attachments`, `created_at`, `completed_at`, and `modified_at`. A deleted task has only `change`, `task_id`, and `list_id`.

Microsoft does not tell who changed a task, so the event has no sender. To read the steps, links, or files of the task, use the commands above with `--list <list_id> --task <task_id>`.

## How the connector watches a list

- With a Trebi Cloud link, Microsoft sends each change at once (live). The connector renews the link before it expires.
- Without a link, the connector asks Microsoft for the changes every 5 minutes (poll).
- A switch between live and poll can repeat one change with another event id. A job that starts from a task event must accept the same change two times. Use `task_id` and `modified_at` to find a repeat.
