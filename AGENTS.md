# AGENTS.md

Guide for AI coding agents working on this repository. Human docs: [README.md](README.md).

## What this is

`garmin-mcp` is a local **stdio MCP server** in Go. It reads the user's own data from the
**Garmin Connect Personal API** (read-only) and exposes it as MCP tools that return compact,
unit-labelled summaries for an LLM to turn into sleep and wellbeing advice.

## Commands

Run these before you say a change is done. CI runs the same checks.

```sh
make test     # go test -race -count=1 ./...  (offline, synthetic fake API)
make lint     # golangci-lint v2.15.0 (pinned, via go run)
make vuln     # govulncheck v1.9.0 (pinned)
make ci-lint  # actionlint for .github/workflows
make all      # lint + test + vuln + build
make live     # real API smoke test; needs .env; never run in CI
```

Go version comes from `go.mod` (`toolchain go1.27.4`). Do not lower it: 1.27.3 has GO-2026-6859 in `net/http`.

## Layout

| Path | Responsibility |
|---|---|
| `cmd/garmin-mcp/` | Flags, config, logging to stderr, stdio server start, `-check` |
| `internal/config/` | Environment variables and the `-env-file` parser |
| `internal/garmin/client.go` | HTTP: key → token exchange, single-flight renewal, retries, size limits |
| `internal/garmin/service.go` | Per-day cache, range chunking under Garmin limits, pagination |
| `internal/garmin/models.go` | Decoding structs, checked against live responses |
| `internal/cache/` | On-disk cache, atomic writes, 0700/0600 |
| `internal/tools/` | MCP tools (`tools.go` registers them), analysis (`recovery.go`, `sleepanalysis.go`, `stats.go`), prompts, raw endpoint allow-list (`raw.go`) |
| `internal/civil/` | Calendar date type |

## Rules

Each rule has a reason. Breaking one is a bug even when the tests pass.

1. **Never read, print or commit `.env`, tokens or real Garmin responses.** They contain the API key and
   health data. Test fixtures must be synthetic. Before committing, run `git diff --cached` and confirm
   no key, JWT (`eyJ...`) or real profile IDs are staged.
2. **Stdout belongs to the MCP protocol.** Log only through `slog` to stderr. A stray `fmt.Println` in
   server code corrupts the JSON-RPC stream. (`-check`, `-version` and `-clear-cache` exit before the server starts, so they may print.)
3. **Never log or return the API key or token**, including inside error messages. `APIError` deliberately
   omits the token endpoint body.
4. **Keep the server read-only.** Add only GET data endpoints. Every tool must set `ReadOnlyHint: true`
   (`TestEveryToolSucceeds` checks this).
5. **Keep credentials on Garmin's host.** Do not remove `CheckRedirect` in `garmin.New`. Do not add env
   or tool inputs that change the base URL; `WithBaseURL` exists for tests only.
6. **`garmin_api_get` is an allow-list.** New raw endpoints go into `rawEndpoints` with validated path
   parameters and allowed query keys. Reproductive-health and nutrition endpoints set `sensitive: true`.
7. **Output types must not serialise a map as `null`.** The SDK validates `structuredContent` against the
   inferred schema: a nil map without `omitempty` fails the call at runtime. Nil slices are fine. Initialise
   maps or tag them `omitempty`.
8. **Optional input fields need `omitempty`** in the `json` tag; without it the inferred schema marks them
   required. `jsonschema` tags hold only the description and must not start with `WORD=`.
9. **Respect Garmin range limits** (HTTP 400 above them): sleep and Body Battery 31 days, stress and steps 28,
   v2 daily summaries 365, HRV 90 (our safe margin). Set `maxSpan` on a `dailyKind`; `Service.daily` chunks.
10. **Do not weaken the cache freshness rule** (`Service.fresh`): data days settle 72 h after the day ends,
    empty days only after 30 days. `TestFreshRule` and `TestEmptyDayIsRefetched` guard it.
11. **Analysis must not overstate.** Compare with the user's own baseline (`newMetric`: needs ≥ 14 days for
    z-scores and signals), show group sizes, hide differences below `minFactorNights`, label server-computed
    heuristics as such and never phrase output as a diagnosis.
12. **Keep code small and plain.** golangci-lint enforces `funlen` (80 lines / 40 statements), `gocognit` (25)
    and `unparam` outside tests. Split a handler into named steps instead of raising a limit; run parallel
    fetches through `fetchAll`; delete code the moment nothing calls it (`go run golang.org/x/tools/cmd/deadcode@latest -test ./...`).
13. **Pin tool versions in both places.** golangci-lint and govulncheck versions live in the `Makefile` and
    `.github/workflows/ci.yml`; CI fails if they differ. Actions are pinned by commit SHA with the tag in a comment.

## Garmin API facts (verified live, Nov 2026)

- Token: `POST /personalapitoken-service/personal-api/personal-api-key/token`, header `Authorization: ApiKey <key>`,
  `Accept` must not be `application/json` only (HTTP 406). Response is a `text/plain` JWT, 24 h, no refresh token.
- Data: `Authorization: Bearer <jwt>`. 204 = no data (`ErrNoData`). 400 = range too long or bad parameter.
  No rate-limit headers are sent.
- Units: weight in grams; activity distance m, duration s, speed m/s.
- `sleepStartTimestampLocal`/`sleepEndTimestampLocal` are epoch ms **already shifted to local time**: format them as UTC.
- Sleep and the Body Battery SLEEP event are filed under the **wake-up** date.
- Not available: Training Readiness, Training Status, Acute Load, VO2max trend.
- The official OpenAPI spec is embedded in the JS bundle of
  <https://developer.garmin.com/gc-developer-program/personal/swagger-docs/>.

## Adding a tool

1. Add or reuse a `Service` method in `internal/garmin` (cached, chunked if it is per-day).
2. Write the handler in `internal/tools` with typed input/output structs (see rules 7 and 8), register it in
   `Register` with read-only annotations and a description that says what it returns, units and limits.
3. Extend the fake server in `internal/tools/tools_test.go`, add a case to `TestEveryToolSucceeds`, add
   invalid inputs to `TestInputValidation`, and add the tool to `internal/tools/live_test.go`.
4. Document it in **both** `README.md` and `README.ru.md` (tool table); keep the two in sync.
5. `make all`, then `make live` if you have a key.

## Releasing

1. Set the new version in `plugin/.claude-plugin/plugin.json` (the release fails if it differs from the tag).
2. `make all`, commit, then `git tag vX.Y.Z && git push origin vX.Y.Z`.
3. `release.yml` builds archives + `garmin-mcp.mcpb`, pushes the ghcr.io image and publishes `server.json`
   (version, image tag and bundle SHA-256 are filled in by the workflow; keep the placeholders in git).
4. Packaging files: `.goreleaser.yaml`, `Dockerfile` (keep the `io.modelcontextprotocol.server.name` label),
   `packaging/mcpb/manifest.json` + `scripts/build-mcpb.sh`, `.claude-plugin/marketplace.json` + `plugin/`.

## Commits

Conventional Commits (`feat:`, `fix:`, `ci:`, `docs:`, `test:`). Small, focused commits. No AI attribution
or `Co-Authored-By` lines.
