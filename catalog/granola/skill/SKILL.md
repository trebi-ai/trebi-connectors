---
name: granola
description: >
  Granola meeting notes through the online Granola server. Use to find meetings in a time range, ask a question across your notes, read the notes of a meeting, list folders, and read a transcript. Triggers: "granola", "meeting notes", "what did we decide in the meeting", "meeting transcript", "notes from my call".
---

# Granola

Granola is an online server. Trebi logs in to Granola for you and adds the login to each call. You do not set a key.

## Tools

The tool list that the server gives at the start of a run is the source of truth. On 2026-10-23 the server had these tools:

| Tool | Use it to |
|---|---|
| `query_granola_meetings` | Ask a question across your meeting notes. |
| `list_meetings` | List the meetings in a time range, with the title and the date. |
| `get_meetings` | Read the notes of one or more meetings by id. |
| `list_meeting_folders` | List your Granola folders. |
| `get_meeting_transcript` | Read the full transcript of one meeting by id. |

## Steps

1. To find a meeting, call `list_meetings` with a time range. Use `query_granola_meetings` when you know the topic but not the date.
2. Read the notes with `get_meetings`. Read the transcript only when the notes do not have the answer, because a transcript is long.
3. Give the meeting title and date with each answer.

## Limits

- Some tools, for example the transcript, can need a paid Granola plan. When a tool says that the plan does not allow it, tell the person. Do not try again.
- The transcript tool has a low rate limit. Do not call it for many meetings in one run.
- When a call says that the login ended, tell the person to log in to Granola again in Trebi.
