---
name: granola
description: >
  Granola meeting notes through the online Granola server. Use to find meetings in a time range, ask a question across your notes, read the notes of a meeting, list folders, and read a transcript. Triggers: "granola", "meeting notes", "what did we decide in the meeting", "meeting transcript", "notes from my call".
---

# Granola

Granola is an online server. Trebi logs in to Granola for you and adds the login to each call. You do not set a key.

## Tools

The tool list that the server gives at the start of a run is the source of truth. On 2026-10-23, after a real login, the server had these tools:

| Tool | Use it to |
|---|---|
| `list_meetings` | List the meetings in a time range (the last 7 days by default), with the title and the date. |
| `search_meetings` | Find meetings by keyword, person, company, or date. It gives the metadata only, not what was said. |
| `query_granola_meetings` | Ask a question across your meeting notes. The answer has links to the notes. |
| `get_meetings` | Read the notes, the summary, and the attendees of one or more meetings by id. |
| `get_meeting_transcript` | Read the transcript of one meeting by id. |
| `get_meeting_attachment` | Get up to 5 images of a meeting, for example screenshots of a shared screen. |
| `list_meeting_folders` | List your Granola folders. |
| `create_folder`, `update_folder` | Make a folder, or change its name or description. |
| `add_note_to_folder`, `remove_note_from_folder` | Put a note in a folder, or take it out. |
| `get_account_info` | Show the Granola account, its workspaces, and its plan. |
| `give_feedback` | Tell Granola about a tool that did not work as its description says. |
| `sign_out` | Do not use it. It ends the login of Trebi. To log out, the person uses Trebi. |

## Steps

1. To find a meeting, call `list_meetings` with a time range, or `search_meetings` with a keyword or a person. Use `query_granola_meetings` for a question across many meetings.
2. Read the notes with `get_meetings`. Read the transcript only when the notes do not have the answer, because a transcript is long.
3. Give the meeting title and date with each answer.

## Limits

- Change folders only when the person asks. A note in a shared folder becomes visible to each person who can open the folder, and a removed note can stop their access.
- Some tools, for example the transcript, can need a paid Granola plan. When a tool says that the plan does not allow it, tell the person. Do not try again.
- The transcript tool has a low rate limit. Do not call it for many meetings in one run.
- When a call says that the login ended, tell the person to log in to Granola again in Trebi.
