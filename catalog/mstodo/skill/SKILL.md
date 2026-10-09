---
name: mstodo-cli
description: >
  Microsoft To Do CLI. Use when the user wants to read To Do lists or tasks, or when a job starts from a task event of the Microsoft To Do connector.
  Triggers: "microsoft to do", "to do list", "todo tasks", "watch a to do list", "task changed in to do".
---

# mstodo-cli — Microsoft To Do CLI

Go CLI for Microsoft To Do through Microsoft Graph. Binary: `mstodo-cli`.

## Login

On its own, `mstodo-cli auth login` shows a code and a Microsoft URL. Open the URL, type the code, and log in. The login is in `~/.cli-tools/mstodo-cli/auth.json`.

In Trebi (`TREBI_STATE_DIR` is set), the login comes only from the Trebi connection. `auth login` fails with "Set this value in Trebi". Log in again from the connection in Trebi.

## Commands

```bash
mstodo-cli lists list                 # the lists: id and name
mstodo-cli tasks list <list id>       # the tasks of one list
mstodo-cli --json lists list          # JSON output
```

## Events

The connector sends one event type, `task`. The room is the list. The data has:

- `change`: `created`, `updated`, or `deleted`.
- `task_id` and `list_id`.
- `title`, `status`, `importance`, `note`, `due`, `completed_at`, and `modified_at`. A deleted task has only `change`, `task_id`, and `list_id`.

Microsoft does not tell who changed a task, so the event has no sender.

## How the connector watches a list

- With a Trebi Cloud link, Microsoft sends each change at once (live). The connector renews the link before it expires.
- Without a link, the connector asks Microsoft for the changes every 5 minutes (poll).
- A switch between live and poll can repeat one change with another event id. A job that starts from a task event must accept the same change two times. Use `task_id` and `modified_at` to find a repeat.
