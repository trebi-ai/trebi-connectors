---
name: bing-cli
description: >
  Go CLI for the Bing Webmaster Tools API. Use for Bing search performance —
  impressions, clicks, average impression/click position — by day, query, or
  page; crawl statistics and HTTP-status breakdowns; crawl/indexing issues;
  per-URL index status and traffic; keyword impression history; verified-site
  management; URL submission and quota. Authenticates with a single Bing
  Webmaster API key. Triggers: "bing webmaster", "bwt", "bing impressions and
  clicks", "bing average position", "bing top queries", "bing top pages",
  "bing crawl stats", "bing crawl issues", "bing indexing issues", "bing url
  info", "submit url to bing", "bing keyword stats", "bing webmaster sites".
---

# bing-cli — Bing Webmaster Tools CLI

Go CLI (`urfave/cli/v3`) wrapping the Bing Webmaster Tools API
(`https://ssl.bing.com/webmaster/api.svc/json`). Tables by default; `-o json` /
`-j` for JSON. Binary: `bing-cli`.
Build: `scripts/build.sh` → `build/bing-cli`.

Each API is a top-level subcommand so the tool can host more Bing APIs later.
Currently: `webmaster` (aliases `wmt`, `bwt`).

## Auth

API key only — **no OAuth**. One-time setup:

1. Sign in to Bing Webmaster Tools (https://www.bing.com/webmasters).
2. Add and verify the site(s) you want data for.
3. Settings (top-right) → API Access → Generate API Key. One key per user; it
   works for all of that user's verified sites.
4. `bing-cli auth login` — prompts for the key (or pass `--api-key`/`-k`),
   stores it in `~/.cli-tools/bing-cli/config.toml` (mode 0600).

Resolution order: flag → env → config file.

- `bing-cli auth status` — verify the key reaches the API.
- `bing-cli auth show` — print config, key redacted.
- `bing-cli auth logout` — delete the config.

Env overrides: `BING_CLI_API_KEY`, `BING_CLI_SITE`, `BING_CLI_CONFIG`,
`BING_CLI_DEBUG=1`.

## Sites

```
bing-cli webmaster sites list                # all verified sites on the account
bing-cli webmaster sites roles [site-url]    # your delegated role(s) for a site
bing-cli webmaster sites add <site-url>
bing-cli webmaster sites remove <site-url>
bing-cli webmaster config set-site <site-url># default for --site
```

Most commands take `--site/-s <url>`; if omitted they fall back to the default
set via `config set-site` (or `BING_CLI_SITE`). URLs are normalized with a
trailing slash.

## Search performance — impressions, clicks, average position

These are the core endpoints. The Bing API returns all available history per
call; `--start` / `--end` (`YYYY-MM-DD`) filter client-side, `--limit`/`-n`
caps printed rows (0 = all).

```
bing-cli webmaster traffic -s <site>                  # daily site-wide impressions+clicks
bing-cli webmaster queries -s <site>                  # top queries: + avg impr/click position
bing-cli webmaster pages -s <site>                    # top pages: + avg impr/click position
bing-cli webmaster queries --page <page-url>          # queries that drove traffic to one page
bing-cli webmaster pages --query "<query>"            # pages a query drove traffic to
```

- `traffic` (alias `rank`) — `GetRankAndTrafficStats`. Columns: DATE,
  IMPRESSIONS, CLICKS. Across all Bing verticals; updates daily.
- `queries` (alias `query`) — `GetQueryStats`, or `GetPageQueryStats` with
  `--page`. Columns: QUERY, DATE, IMPRESSIONS, CLICKS, AVG IMPR POS, AVG CLICK
  POS. Updates weekly.
- `pages` — `GetPageStats`, or `GetQueryPageStats` with `--query`. Same columns
  keyed by PAGE.

## Crawl & indexing health

```
bing-cli webmaster crawl -s <site>            # daily crawl stats + HTTP-code breakdown
bing-cli webmaster issues -s <site>           # URLs flagged with crawl/indexing problems
```

- `crawl` — `GetCrawlStats`. Columns: DATE, CRAWLED, IN INDEX, ERRORS, 2XX,
  3XX, 4XX, 5XX, ROBOTS-BLOCKED. `--start`/`--end`/`--limit` as above.
- `issues` (alias `crawl-issues`) — `GetCrawlIssues`. Columns: URL, HTTP, IN
  LINKS, ISSUES. The API's `Issues` field is a bitmask; the CLI decodes it into
  flag names (`Code4xx`, `Code5xx`, `BlockedByRobotsTxt`, `ContainsMalware`,
  etc.).

## Per-URL inspection

```
bing-cli webmaster url info <page-url> -s <site>      # index status of one page
bing-cli webmaster url traffic <page-url> -s <site>   # traffic + index summary of one page
```

- `url info` — `GetUrlInfo`: HTTP status, discovery/last-crawled dates,
  document size, anchor-text and child-URL counts.
- `url traffic` — `GetUrlTrafficInfo`: impressions, clicks, in-index count.

## Keyword impression history

```
bing-cli webmaster keyword "<keyword>" --country US --language en-US
```

`GetKeywordStats` — account-wide Bing impression volume for a keyword by date
(no site needed). `--country` (2-letter) and `--language` narrow the result;
`--start`/`--end`/`--limit` filter as above.

## URL submission

```
bing-cli webmaster quota -s <site>            # remaining daily/monthly submission quota
bing-cli webmaster submit <page-url> -s <site># ask Bing to crawl a URL
```

`submit` (`SubmitUrl`) counts against the quota shown by `quota`
(`GetUrlSubmissionQuota`).

## Notes

- All read endpoints are GET; `AddSite`, `RemoveSite`, `SubmitUrl` are POST.
- Bing returns dates as `/Date(epoch±offset)/`; the CLI decodes them to
  `YYYY-MM-DD` (and to ISO dates in JSON output).
- Error `InvalidApiKey` (ErrorCode 3) means the key is wrong or missing — re-run
  `auth login`. Errors come back as HTTP 400 with `{"ErrorCode":N,"Message":...}`.
- Traffic/query data is not real-time: site traffic updates daily, query stats
  weekly.
