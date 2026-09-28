---
name: keywords-everywhere-cli
description: >
  Keywords Everywhere API CLI (https://api.keywordseverywhere.com/docs/). Use when the user
  wants keyword search volume, CPC, competition or 12-month trend data; related or "People Also
  Search For" keyword suggestions; the keywords a domain or URL ranks for; estimated traffic
  metrics for domains/URLs; or backlink data. Authenticates with a single Keywords Everywhere
  API key. Triggers: "keywords everywhere", "keyword search volume", "cpc for keyword",
  "related keywords", "people also search for", "domain keywords", "url keywords",
  "estimated traffic for domain", "backlinks for domain", "ke api".
---

# keywords-everywhere-cli — Keywords Everywhere API CLI

Go CLI wrapping the Keywords Everywhere API v1. JSON output, pretty-printed.
Binary: `keywords-everywhere-cli`.
Build: `scripts/build.sh` → `build/keywords-everywhere-cli`.

## Auth

Keywords Everywhere uses a single API key sent as a Bearer token. Get it from
https://keywordseverywhere.com/first-install-addon.html.

Resolution order: `--api-key` flag → `KEYWORDS_EVERYWHERE_API_KEY` env → `~/.cli-tools/keywords-everywhere-cli/config.json` (mode 0600).

```bash
keywords-everywhere-cli auth set --api-key XXXX [--api-url URL]
keywords-everywhere-cli auth status            # validates against /account/credits
keywords-everywhere-cli auth show              # config with API key masked
keywords-everywhere-cli auth clear             # removes config file
keywords-everywhere-cli credits                # account credit balance
```

## Global flags

| Flag | Env | Purpose |
| --- | --- | --- |
| `--api-key` | `KEYWORDS_EVERYWHERE_API_KEY` | API key |
| `--api-url` | `KEYWORDS_EVERYWHERE_API_URL` | base URL (default `https://api.keywordseverywhere.com/v1`) |
| `--compact` | — | single-line JSON output |

## Commands

### Miscellaneous

```bash
keywords-everywhere-cli credits        # GET /account/credits — array with one balance integer
keywords-everywhere-cli countries      # GET /countries — { "us": "United States", ... }
keywords-everywhere-cli currencies     # GET /currencies — { "usd": "United States Dollar", ... }
```

`country` and `currency` flags on the data commands take the lowercase codes from
`countries` / `currencies` (e.g. `us`, `uk`, `usd`, `gbp`). An empty country means global data.

### Keyword data — `keyword`

```bash
# POST /get_keyword_data — volume, CPC, competition, 12-month trend. 1 credit per keyword.
keywords-everywhere-cli keyword data --kw 'seo tools' --kw 'keyword research' \
    [--country us] [--currency usd] [--data-source cli|gkp]

# POST /get_pasf_keywords — "People Also Search For" suggestions
keywords-everywhere-cli keyword pasf --keyword 'climate change' [--num 20]

# POST /get_related_keywords — related keyword suggestions
keywords-everywhere-cli keyword related --keyword 'climate change' [--num 20]

# POST /get_domain_keywords — keywords a domain ranks for (keyword, traffic, SERP position)
keywords-everywhere-cli keyword domain --domain example.com [--country us] [--num 100]

# POST /get_url_keywords — keywords a single URL ranks for
keywords-everywhere-cli keyword url --url https://example.com/page [--country us] [--num 50]
```

- `--kw` is repeatable, up to 100 keywords per call.
- `--data-source`: `cli` = Google Keyword Planner + Clickstream (default); `gkp` = GKP only.
- `--num` range is 1–10000 for `domain`/`url`.

### Traffic metrics — `traffic`

```bash
# POST /get_domain_traffic_metrics — estimated_monthly_traffic + total_ranking_keywords
keywords-everywhere-cli traffic domain --domain example.com --domain example.org [--country us]

# POST /get_url_traffic_metrics — same metrics, per URL
keywords-everywhere-cli traffic url --url https://example.com/ [--country us]
```

`--domain` and `--url` are repeatable.

### Backlink data — `backlinks`

Each row has `anchor_text`, `domain_source`, `domain_target`, `url_source`, `url_target`.
The `unique-*` variants deduplicate by source domain.

```bash
keywords-everywhere-cli backlinks domain        --domain example.com [--num 100]   # POST /get_domain_backlinks
keywords-everywhere-cli backlinks unique-domain --domain example.com [--num 100]   # POST /get_unique_domain_backlinks
keywords-everywhere-cli backlinks page          --page https://example.com/ [--num 50]  # POST /get_page_backlinks
keywords-everywhere-cli backlinks unique-page   --page https://example.com/ [--num 50]  # POST /get_unique_page_backlinks
```

`--num` range is 1–10000.

## Output

Pretty-printed JSON by default; `--compact` for single-line. Pipe through `jq`:

```bash
keywords-everywhere-cli keyword data --kw 'seo' --country us \
    | jq '.data[] | {keyword, vol, cpc: .cpc.value, competition}'
keywords-everywhere-cli credits | jq '.[0]'
```

Successful data responses include `credits` / `credits_consumed` (or `credits_consumed` +
`time_taken`) so you can track spend.

## Errors

- `401` — missing or invalid API key.
- `402` — invalid subscription or insufficient credits.
- `400` — invalid request data.

Errors are returned as `{"message": "..."}` and surfaced as `keywords-everywhere: <status> <text>: <message>`.
