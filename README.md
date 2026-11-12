# garmin-mcp

[![CI](https://github.com/sasha-sup/garmin-mcp/actions/workflows/ci.yml/badge.svg)](https://github.com/sasha-sup/garmin-mcp/actions/workflows/ci.yml)

**English** | [Русский](README.ru.md)

An MCP server that gives Claude (or any other MCP client) **read-only** access to your own
Garmin Connect data: sleep, HRV, heart rate, stress, Body Battery, daily activity, workouts and weight.
It uses the official [Garmin Connect Personal API](https://developer.garmin.com/gc-developer-program/personal/personal-api/).

The main use case is personal advice on sleep and wellbeing. The server computes averages, sleep
regularity, sleep shortfall, deviations from your own baseline and links between your day and your
sleep; Claude turns that into concrete recommendations.

> **MCP** (Model Context Protocol) is the standard an AI assistant uses to call external tools.
> Claude Code starts `garmin-mcp` as a child process and talks to it over stdin/stdout.

## Quick start

1. **API key.** Open <https://connect.garmin.com/app/settings/personalApiToken> → *Create Key* →
   accept the terms → choose an expiry (30, 90, 180 days or 1 year) → copy the key.
   The key is shown **once**. Garmin emails you about a week before it expires.

2. **Build** (Go 1.27.4+; an older Go 1.21+ downloads the right toolchain automatically):

   ```sh
   make build            # binary in bin/garmin-mcp
   ```

3. **Configure:**

   ```sh
   cp .env.example .env
   chmod 600 .env        # only your user can read the key
   $EDITOR .env          # set GARMIN_API_KEY and GARMIN_TZ
   ```

4. **Check:**

   ```sh
   ./bin/garmin-mcp -env-file .env -check
   # auth ok: access token valid until 2026-11-13 10:33:12
   # profile ok: Garmin time zone Europe/Berlin, server time zone Europe/Berlin (CET +01:00)
   # data ok: 7 of the last 7 days have a daily summary
   ```

5. **Connect to Claude Code** (for all projects: `-s user`):

   ```sh
   claude mcp add -s user garmin -- "$PWD/bin/garmin-mcp" -env-file "$PWD/.env"
   ```

   Claude Desktop, in `claude_desktop_config.json`:

   ```json
   {
     "mcpServers": {
       "garmin": {
         "command": "/absolute/path/bin/garmin-mcp",
         "args": ["-env-file", "/absolute/path/.env"]
       }
     }
   }
   ```

6. **Ask Claude**, for example:
   - "Analyse my sleep over the last month and give me recommendations"
   - "How recovered am I today? Is a hard workout a good idea?"
   - "How have my resting heart rate and HRV changed over six months?"
   - "What hurts my sleep more: stress or late workouts?"

   Or run a ready-made prompt. In Claude Code they are `/mcp__garmin__sleep_review`,
   `/mcp__garmin__wellbeing_review` and `/mcp__garmin__readiness_check` (optionally with a number of days).

## Tools

| Tool | Returns | Period |
|---|---|---|
| `garmin_sleep_analysis` | Sleep analysis: average duration vs Garmin's baseline sleep need and weekly shortfall, median sleep onset and wake time and their spread, weekday/weekend shift (social jetlag), stage shares, sleep score trend, sleep heart rate/HRV/SpO2/breathing, and **factors** from the previous day (stress above your usual, late or hard workouts, low step count, late sleep onset → score, HRV and sleep heart rate with and without; differences are shown only with ≥ 5 nights in each group) | up to 366 days, default 28 |
| `garmin_sleep` | Per night: bedtime, wake time, stages, score and sub-scores, heart rate, stress, SpO2, breathing, sleep need, Garmin's feedback | up to 366, 7 |
| `garmin_recovery_status` | One-day readiness snapshot: sleep; HRV, resting HR, sleep HR, breathing rate and yesterday's stress each compared with **your own** previous 28 days (mean, SD, z-score, days of history); overnight Body Battery charge; training load of the last 7 vs 28 days; plain-language signals and the method used. Baseline-relative signals need ≥ 14 days of history | 1 day |
| `garmin_trends` | Weekly or monthly averages: resting HR, HRV, sleep, steps, stress, Body Battery, intensity minutes, workouts, training load, weight | up to 731 days, 84 |
| `garmin_daily_summary` | Per day: steps, distance, calories, heart rate, intensity minutes, stress, Body Battery, breathing, SpO2 | up to 366, 7 |
| `garmin_hrv` | Overnight HRV, 7-day average, personal baseline band, Garmin status | up to 366, 14 |
| `garmin_body_battery` | Charged/drained, min/max, events (sleep, workouts), Garmin's feedback, optional intraday timeline | up to 31, 3 |
| `garmin_heart_rate` | Heart rate for one day in local-time intervals (5, 10, 15, 20, 30, 60, 120, 180, 240 min) | 1 day |
| `garmin_activities` | Workouts: type, duration, distance, pace/speed, heart rate, time in zones, training load, training effect, VO2max | default 30 days, up to 100 items |
| `garmin_activity` | Full summary of one workout | — |
| `garmin_profile` | Time zone, heart rate zones, Fitness Age, latest weight | — |
| `garmin_weight` | Weigh-ins: weight, BMI, body fat, muscle mass, body water | up to 731, 90 |
| `garmin_api_get` | Raw JSON from an allow-listed endpoint (hourly SpO2, daily stress and steps, hydration, …), truncated at 100 KB. Nutrition and menstrual cycle only with `GARMIN_ALLOW_SENSITIVE=1` | — |

All dates are **local calendar dates** (`YYYY-MM-DD`). Sleep is filed under the **wake-up** date:
the night from 10 to 11 November is `2026-11-11`.

### Not in the API

The Personal API does not expose Training Readiness, Training Status, Acute Load or a VO2max trend.
`garmin_recovery_status` computes comparable signals itself (HRV, resting HR, sleep, the sum of
`activityTrainingLoad` over 7 and 28 days) and labels them as computed by this server, not by Garmin.

## Configuration

| Variable | Purpose | Default |
|---|---|---|
| `GARMIN_API_KEY` (or `GARMIN_API`) | Personal API key | — (required) |
| `GARMIN_TZ` | Your IANA time zone, e.g. `Europe/Berlin`. Decides what "today" is. `garmin_profile` warns if it differs from your Garmin profile | system zone |
| `GARMIN_CACHE_DIR` | Cache directory, or `off` | user cache dir: `~/.cache/garmin-mcp` on Linux, `~/Library/Caches/garmin-mcp` on macOS |
| `GARMIN_REGION` | `global` or `cn` | `global` |
| `GARMIN_LOG_LEVEL` | `debug`, `info`, `warn`, `error` (logs go to stderr) | `info` |
| `GARMIN_ALLOW_SENSITIVE` | `1` enables the nutrition and menstrual-cycle endpoints in `garmin_api_get` | off |

Flags: `-env-file PATH` (the environment wins over the file; the file may only set `GARMIN_*`), `-check`, `-clear-cache`, `-version`.

## How it works

- **Authentication.** The key is exchanged for an access token (a JWT valid for 24 hours; there is
  no refresh token): `POST /personalapitoken-service/personal-api/personal-api-key/token` with
  `Authorization: ApiKey <key>`. Every other request carries `Authorization: Bearer <token>`.
  The token lives only in memory and is renewed 5 minutes before expiry, and once on HTTP 401.
  One request performs the renewal; concurrent requests wait for it, but never longer than their own timeout.
- **Range limits.** Garmin answers `400` when a period is too long: sleep and Body Battery 31 days,
  stress and steps 28, daily summaries 1 year. The dedicated tools split requests into chunks and run
  them in parallel (at most 4 at a time). `garmin_api_get` passes dates through unchanged, so raw
  `stress_daily`/`steps_daily` requests must stay within 28 days.
- **Cache.** Responses are stored per day in `GARMIN_CACHE_DIR`. A day with data that was fetched
  more than 72 hours after it ended is final and never requested again; more recent days are cached
  for 10 minutes because the watch keeps syncing. A day **without data** (watch not worn or not
  synced yet) is re-checked every 6 hours and becomes final only after 30 days. Re-analysing a year
  costs a handful of requests, not hundreds. If the watch synced very late and past days came in
  partially, run `garmin-mcp -clear-cache`.
- **Retries.** On `429` and `5xx`: up to 3 retries with exponential backoff, honouring `Retry-After`.
  Garmin does not document a rate limit.

## Privacy and security

- The server **only reads**: the API has no write methods and every tool is marked `readOnlyHint`.
- The key and token are sent only to Garmin's host; redirects are not followed, so the key cannot
  leak to another address. Logs and error messages never contain the key or token.
- The cache holds health data: the `v1` directory inside `GARMIN_CACHE_DIR` gets mode `0700`, files
  `0600`; symlinks and oversized files in the cache are ignored. Clear it with `garmin-mcp -clear-cache`
  or by deleting the directory.
- `-env-file` accepts only `GARMIN_*` variables, so a tampered file cannot set `HTTPS_PROXY` or
  `SSL_CERT_FILE` to intercept the key.
- Nutrition and menstrual-cycle data never reach the model unless you set `GARMIN_ALLOW_SENSITIVE=1`.
- Whatever the tools return is sent to the model provider (Anthropic for Claude) as part of the conversation.
- `.env` and `*.db` are in `.gitignore`. If the key leaks, delete it on the Personal API Key page;
  every token issued for it stops working too.
- Recommendations based on this data are not medical advice.

## Development

```sh
make test    # unit tests with -race against a synthetic fake Garmin API (no real data needed)
make lint    # golangci-lint v2
make vuln    # govulncheck
make live    # every tool against the real API (needs .env); prints response sizes only
make all     # lint + test + vuln + build
make ci-lint # validate the GitHub Actions workflows (actionlint)
```

### CI

GitHub Actions (`.github/workflows/ci.yml`) runs on pushes to `main`, on pull requests, weekly
(new vulnerabilities appear without code changes) and on demand:

| Job | What it does |
|---|---|
| Lint | `go mod tidy` leaves `go.mod`/`go.sum` unchanged; golangci-lint v2.15.0 with `.golangci.yml` |
| Test | `go vet`, `go test -race` with coverage on Ubuntu and macOS, builds the binary |
| Vulnerability scan | `govulncheck` over the code and its dependencies |

Actions are pinned to commit SHAs, the workflow has only `contents: read`, and checkout does not
persist the token. Dependabot proposes Go module and action updates weekly. The live test against
the real API does not run in CI: no key is given to CI.

Layout:

```
cmd/garmin-mcp/      entry point: flags, config, stdio server
internal/config/     environment and .env loading
internal/garmin/     HTTP client (token, retries, limits), response models, cached range-aware service
internal/cache/      file cache with atomic writes
internal/tools/      MCP tools, sleep and recovery analysis, prompts
internal/civil/      calendar date without time
```

Garmin's official OpenAPI spec is embedded in
<https://developer.garmin.com/gc-developer-program/personal/swagger-docs/>; the response formats in
`internal/garmin/models.go` were checked against live API responses (November 2026).
