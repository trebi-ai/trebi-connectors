---
name: google-cli
description: >
  Go CLI for Google APIs — Search Console (Webmaster Tools) and PageSpeed
  Insights. Use for Search Console search performance — impressions, clicks,
  CTR, average position — by query/page/country/device/date; URL indexing
  status and coverage issues; sitemaps; property management. Also use for
  PageSpeed Insights: Lighthouse performance/accessibility/SEO scores, Core Web
  Vitals (LCP/INP/CLS) lab and field data, and audit results for any public
  URL. Search Console uses OAuth 2.0; PageSpeed needs no auth. Triggers:
  "search console", "gsc", "impressions and clicks", "average position",
  "top queries", "indexing issues", "url inspection", "submit sitemap",
  "pagespeed", "page speed", "psi", "lighthouse score", "core web vitals",
  "web vitals", "LCP", "performance score for url".
---

# google-cli — Google APIs CLI (Search Console + PageSpeed)

Go CLI (`urfave/cli/v3`) wrapping Google APIs. Tables by default; `-o json` /
`-j` for JSON. Binary: `google-cli`.
Build: `scripts/build.sh` → `build/google-cli`.

Each API is a top-level subcommand so the tool can host more Google APIs:
- `search-console` (aliases `sc`, `gsc`) — needs OAuth 2.0.
- `pagespeed` (aliases `psi`, `ps`) — needs no auth.

## Auth

OAuth 2.0 only — **no API key**. One-time setup:

1. In Google Cloud Console: create/select a project, enable the "Google Search
   Console API", configure the OAuth consent screen (External; add the user's
   account as a Test user).
2. Create an OAuth client ID of type **Desktop app**.
3. `google-cli auth login` — prompts for client ID/secret (or pass
   `--client-id`/`--client-secret`), opens a browser for consent, captures a
   refresh token into `~/.cli-tools/google-cli/config.toml` (mode 0600).

The refresh token mints access tokens automatically; refreshed tokens are
written back to the config. Resolution order: flag → env → config file.

- `google-cli auth status` — verify the token reaches the API.
- `google-cli auth show` — print config, secrets redacted.
- `google-cli auth logout` — delete the config.

Env overrides: `GOOGLE_CLI_CLIENT_ID`, `GOOGLE_CLI_CLIENT_SECRET`,
`GOOGLE_CLI_REFRESH_TOKEN`, `GOOGLE_CLI_SITE`, `GOOGLE_CLI_CONFIG`,
`GOOGLE_CLI_DEBUG=1`.

## Properties (sites)

A property is a URL-prefix property (`https://example.com/`, **trailing slash
required**) or a domain property (`sc-domain:example.com`). The CLI normalizes
a missing trailing slash on URL-prefix forms automatically.

```
google-cli sc sites list
google-cli sc sites get <site-url>
google-cli sc sites add <site-url>
google-cli sc sites delete <site-url>
google-cli sc config set-site <site-url>   # default for --site
```

Most commands take `--site/-s <url>`; if omitted they fall back to the default
property set via `config set-site` (or `GOOGLE_CLI_SITE`).

## Search analytics — impressions, clicks, CTR, position

`google-cli sc analytics` (aliases `perf`, `query`). This is the endpoint for
performance metrics.

```
google-cli sc analytics -s <site> --dimensions query
google-cli sc analytics --dimensions date --start 2026-04-01 --end 2026-04-30
google-cli sc analytics --dimensions page --device MOBILE --country usa -n 100
```

Flags:
- `--start` / `--end` — `YYYY-MM-DD`; default range is the last 28 days.
- `--dimensions` / `-d` — repeatable; one of `query, page, country, device,
  date, searchAppearance`. Output groups by these.
- `--type` — `web` (default), `image`, `video`, `news`, `discover`, `googleNews`.
- `--limit` / `-n` — rows, 1–25000 (default 20). `--start-row` paginates.
- `--data-state` — `final` (default) or `all` (includes fresh, not-yet-final data).
- `--aggregation` — `auto`, `byPage`, `byProperty`.
- Convenience filters: `--device`, `--country` (3-letter code), `--query-contains`,
  `--page-contains`.

Each row reports CLICKS, IMPRESSIONS, CTR, POSITION (average position). JSON
output returns raw `rows` with `keys`, `clicks`, `impressions`, `ctr`,
`position`.

## URL inspection — indexing issues

`google-cli sc inspect <url>` (aliases `index`, `url`). Reports the indexing
verdict, coverage state (why a page is or isn't indexed), crawl status,
canonical mismatches (Google vs. user canonical), and mobile-usability /
rich-results / AMP issues. `--language` sets the BCP-47 code for issue messages.

```
google-cli sc inspect https://example.com/pricing -s https://example.com/
```

## Sitemaps

```
google-cli sc sitemaps list
google-cli sc sitemaps get <feedpath>
google-cli sc sitemaps submit <feedpath>
google-cli sc sitemaps delete <feedpath>
```

`<feedpath>` is the full sitemap URL (e.g. `https://example.com/sitemap.xml`).
List/get report pending state, warnings, errors, and per-content-type indexed
counts.

## PageSpeed Insights — `google-cli pagespeed` (aliases `psi`, `ps`)

Analyzes a public URL with the PageSpeed Insights API v5. **No auth required.**
An API key only raises the daily quota; set `GOOGLE_CLI_PAGESPEED_KEY` or
`pagespeed_key` in the config. Keyless requests share a small anonymous quota
and can return HTTP 429.

Every subcommand takes `<url>` plus `--strategy`/`-s` (`MOBILE` default or
`DESKTOP`) and `--locale`/`-l`. One analysis is run per invocation; the
subcommand picks which slice of the result to show.

- `psi analyze <url>` (alias `run`) — full report: summary + scores + Core Web
  Vitals. `--category`/`-c` repeatable (default all four categories).
- `psi scores <url>` — Lighthouse category scores (0-100) with good / needs
  improvement / poor ratings. `--category`/`-c` to limit.
- `psi metrics <url>` (aliases `cwv`, `vitals`) — Core Web Vitals: real-world
  CrUX field data (LCP, INP, CLS, FCP, TTFB at p75) plus Lighthouse lab
  metrics. Field data may be absent for low-traffic pages.
- `psi audits <url>` — individual Lighthouse audits, failing ones first;
  `--all` includes passing/non-applicable audits. `--category`/`-c` to limit.

Categories: `PERFORMANCE`, `ACCESSIBILITY`, `BEST_PRACTICES`, `SEO` (hyphens
also accepted, e.g. `best-practices`).

```
google-cli psi scores https://example.com --strategy desktop
google-cli psi metrics https://example.com
google-cli psi audits https://example.com -c performance
google-cli psi analyze https://example.com -j
```

## Notes

- Search Console data lags ~2-3 days; use `--data-state all` for fresher,
  not-yet-final figures.
- 403 on a property usually means the OAuth account lacks access to it, or the
  consent screen is missing the `webmasters` scope — re-run `auth login`.
- PageSpeed 429 (`RESOURCE_EXHAUSTED`) means the keyless shared quota is spent;
  supply an API key via `GOOGLE_CLI_PAGESPEED_KEY`.
