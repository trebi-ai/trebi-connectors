---
name: data-for-seo-cli
description: >
  DataForSEO REST API CLI (https://docs.dataforseo.com/v3/). Use when the user wants SERP results,
  keyword research, backlink analysis, on-page SEO audits, domain technology lookups, content
  sentiment, merchant/Amazon product data, app-store data, business listings/reviews, or LLM
  optimization (LLM responses, LLM mentions). Triggers: "dataforseo", "serp google",
  "keyword search volume", "backlinks for domain", "on-page audit", "amazon asin", "trustpilot
  reviews", "google my business reviews", "llm mentions", "dfs labs".
---

# data-for-seo-cli — DataForSEO REST API CLI

Go CLI wrapping the DataForSEO v3 REST API. JSON output, pretty-printed.
Binary: `data-for-seo-cli`.
Build: `scripts/build.sh` → `build/data-for-seo-cli`.

## Auth

DataForSEO uses HTTP Basic auth with `api_login` (email) + `api_password` from the dashboard.

Resolution order: `--login`/`--password` flags → `DATAFORSEO_LOGIN`/`DATAFORSEO_PASSWORD` env → `~/.cli-tools/data-for-seo-cli/config.json`.

```bash
data-for-seo-cli auth set --login you@example.com --password XXXX [--api-url URL]
data-for-seo-cli auth status                                          # validates against /v3/appendix/user_data
data-for-seo-cli auth show                                            # config with password masked
data-for-seo-cli auth clear                                           # removes config file
data-for-seo-cli whoami                                               # full account info (balance, rates, quotas)
```

## Global flags

| Flag | Env | Purpose |
| --- | --- | --- |
| `--login` | `DATAFORSEO_LOGIN` | api_login (email) |
| `--password` | `DATAFORSEO_PASSWORD` | api_password |
| `--api-url` | `DATAFORSEO_API_URL` | base URL (default `https://api.dataforseo.com/v3`) |
| `--sandbox` | `DATAFORSEO_SANDBOX` | flip host to `sandbox.dataforseo.com` (free dummy data, identical schema) |
| `--compact` | — | single-line JSON output |

## Command tree

```
data-for-seo-cli auth        set | status | show | clear
data-for-seo-cli whoami                                     # shortcut for appendix user-data
data-for-seo-cli appendix    user-data | status | errors | api-errors | id-list |
                             tasks-fixed | sandbox | ai-optimized-response | webhook-resend
data-for-seo-cli serp        <engine> <feature> live|task     (google, bing, youtube, baidu, yahoo, seznam, naver)
                             <engine> locations [--country XX] | languages
data-for-seo-cli ai          llm-responses <provider> live|task
                             llm-scraper <provider> live|task
                             ai-keyword-data keywords-search-volume
                             llm-mentions <action>            (live only)
data-for-seo-cli keywords    google-ads | bing | google-trends | dataforseo-trends |
                             clickstream-data — each <endpoint> live|task
data-for-seo-cli labs        google | amazon | google-play | app-store — each <endpoint> (all live)
data-for-seo-cli backlinks   summary | history | backlinks | anchors | bulk-* | timeseries-* | ...
data-for-seo-cli onpage      task-post | tasks-ready | force-stop |
                             summary | pages | resources | links | lighthouse | ... |
                             instant-pages | page-screenshot
data-for-seo-cli domain      technologies <endpoint> | whois overview
data-for-seo-cli content     search | summary | sentiment-analysis | phrase-trends | ...
data-for-seo-cli merchant    google <noun> task | amazon <noun> task
data-for-seo-cli app         google <action> task|live | apple <action> task|live
data-for-seo-cli business    listings <endpoint> | google <endpoint> task|live |
                             trustpilot <noun> task | tripadvisor <noun> task | social-media <platform>
```

Every command's `--help` lists its subcommands; explore with `data-for-seo-cli <group> --help`.

## How requests work — `--body` pass-through

DataForSEO endpoints accept a **JSON array** body (one task per element, even singletons). Per-endpoint param schemas are too numerous to model as flags, so the CLI takes the body verbatim via `--body`:

```bash
--body '[{"keyword":"hello","location_code":2840,"language_code":"en"}]'   # inline
--body @./task.json                                                         # file
--body -                                                                    # stdin
```

The body is JSON-validated locally before sending. Schemas: `https://docs.dataforseo.com/v3/<group>/<endpoint>/`.

## Async task lifecycle

Task-based groups follow `task post → tasks ready (poll) → task get <variant> <id>`:

```bash
# 1. submit
data-for-seo-cli serp google organic task post \
    --body '[{"keyword":"machine learning","location_code":2840,"language_code":"en"}]'
# → returns task id in tasks[].id

# 2. poll for completion (cheap)
data-for-seo-cli serp google organic task ready

# 3. fetch results
data-for-seo-cli serp google organic task get --variant advanced --id <id>
```

Variants are endpoint-specific: typically `regular`, `advanced`, `html`. `data-for-seo-cli <…> task get --help` lists what's accepted.

Live (synchronous) variant of the same endpoint — costs more, returns inline:

```bash
data-for-seo-cli serp google organic live --variant advanced \
    --body '[{"keyword":"machine learning","location_code":2840,"language_code":"en"}]'
```

Live-only groups (no task endpoints): `labs`, `backlinks`, `domain`, `content`, and several `ai` subcommands.

## Examples per group

```bash
# SERP — Google organic, live
data-for-seo-cli serp google organic live --variant advanced \
    --body '[{"keyword":"go programming","location_code":2840,"language_code":"en","device":"desktop"}]'

# SERP — YouTube video info, task flow
data-for-seo-cli serp youtube video-info task post --body '[{"video_id":"dQw4w9WgXcQ"}]'
data-for-seo-cli serp youtube video-info task ready
data-for-seo-cli serp youtube video-info task get --variant advanced --id <id>

# Keyword search volume (Google Ads)
data-for-seo-cli keywords google-ads search-volume live \
    --body '[{"keywords":["ai","machine learning"],"location_code":2840,"language_code":"en"}]'

# DataForSEO Labs — related keywords (live only)
data-for-seo-cli labs google related-keywords \
    --body '[{"keyword":"seo tools","language_code":"en","location_code":2840,"limit":50}]'

# Backlinks — summary (live only)
data-for-seo-cli backlinks summary --body '[{"target":"example.com"}]'
data-for-seo-cli backlinks bulk-ranks --body '[{"targets":["a.com","b.com","c.com"]}]'

# OnPage — start a crawl, then pull a summary
data-for-seo-cli onpage task-post --body '[{"target":"example.com","max_crawl_pages":100}]'
data-for-seo-cli onpage tasks-ready
data-for-seo-cli onpage summary --body '[{"id":"<task_id>"}]'

# OnPage — single-page Lighthouse, live
data-for-seo-cli onpage lighthouse live-json --body '[{"url":"https://example.com"}]'

# Domain tech stack (live)
data-for-seo-cli domain technologies domains-by-technology \
    --body '[{"technologies":["nextjs"],"limit":100}]'

# Content sentiment (live)
data-for-seo-cli content sentiment-analysis --body '[{"keyword":"new product launch"}]'

# Amazon ASIN
data-for-seo-cli merchant amazon asin task post \
    --body '[{"asin":"B08N5WRWNW","location_code":2840,"language_code":"en"}]'

# Google Play app info, task flow
data-for-seo-cli app google app-info task post --body '[{"app_id":"com.google.android.youtube"}]'

# Trustpilot reviews
data-for-seo-cli business trustpilot reviews task post --body '[{"domain":"example.com","depth":50}]'

# LLM responses via ChatGPT
data-for-seo-cli ai llm-responses chat-gpt live --variant advanced \
    --body '[{"user_prompt":"List the top 5 SEO tools","model_name":"gpt-4o"}]'

# Account info / credits
data-for-seo-cli whoami | jq '.tasks[0].result[0].money.balance'
```

## Sandbox

`--sandbox` (or `DATAFORSEO_SANDBOX=1`) rewrites `api.dataforseo.com` → `sandbox.dataforseo.com`. Identical auth, paths, schema. Free, returns dummy data, charged at $0 — perfect for shaping bodies before spending credits.

```bash
data-for-seo-cli --sandbox serp google organic live --variant advanced \
    --body '[{"keyword":"hello","location_code":2840,"language_code":"en"}]'
```

## Output

JSON, pretty-printed by default. Add `--compact` for single-line. Pipe through `jq` to navigate the nested `tasks[].result[]` shape:

```bash
data-for-seo-cli serp google organic live --variant advanced --body @body.json \
    | jq '.tasks[].result[].items[] | {title, url, rank_absolute}'
```

## Common gotchas

- **Body is always a JSON array**, even for one task: `[{...}]`, not `{...}`. The CLI does not auto-wrap.
- **status_code ≠ HTTP status**. DataForSEO returns its own `status_code` (e.g. `20000` for ok). HTTP 200 with `tasks[].status_code: 40000+` means the task itself errored — `jq '.tasks[].status_message'` to inspect.
- **No pagination in CLI.** Use `limit`/`offset` fields in the request body; re-submit with the next offset.
- **Credentials are stored plaintext** in `~/.cli-tools/data-for-seo-cli/config.json` (mode 0600). Prefer env vars on shared machines.
- **Locations/languages are per-engine**: `data-for-seo-cli serp google locations` ≠ `data-for-seo-cli keywords google-ads locations`. Use the helper subcommands per group.

## Where things live

- Build script: `scripts/build.sh` → `build/data-for-seo-cli`
- Config file: `~/.cli-tools/data-for-seo-cli/config.json` (chmod 600)
- Auth code: `cmd/auth.go`
- Subcommand files: `cmd/{serp,ai,keywords,labs,backlinks,onpage,domain,content,merchant,app,business,appendix,whoami}.go`
