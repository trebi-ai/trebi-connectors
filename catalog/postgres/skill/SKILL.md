---
name: postgres
description: >
  Read a PostgreSQL database through Trebi. Use when the user asks for numbers, rows, or a report from the database, or wants a live artifact with database data.
  Triggers: "query the database", "how many rows", "sql report", "postgres".
---

# PostgreSQL

Trebi runs each query for you with `trebi connector call <connection> query`. You do not get the password. The section "Operations" lists the inputs.

## Rules

- The session is read-only. A query that writes fails. Do not try to change data.
- Each query stops after 30 seconds. Add `LIMIT` to a query that can return many rows.
- The output is CSV: the first row has the column names.
- Put a text value in single quotes and double each single quote in it. The session is read-only, but a bad value can still change the result.
- Read the schema first when you do not know the tables.

```bash
trebi connector call postgres query --input sql="select table_name from information_schema.tables where table_schema = 'public' order by 1" --json
trebi connector call postgres query --input-json q.json --json
```

`q.json`:

```json
{"sql": "select id, total from orders where created_at >= '2026-10-01' order by created_at desc limit 50"}
```
