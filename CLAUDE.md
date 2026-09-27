# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Architecture

The Pi Go server fetches Google Calendar events, renders an 800×480 1-bit image with `fogleman/gg`, packs it to 48000 bytes (MSB-first, bit=1=white, bit=0=black), and serves it at `/calendar.bin`. The ESP32 wakes every 30 minutes, fetches that URL with `?bat=NN&rssi=NN` query params (so the server can render battery and WiFi state into the status bar), then pushes the buffer to the Waveshare display via `drawInvertedBitmap(..., GxEPD_BLACK)`.

```
                    HTTP fetch every 30 min
  ┌──────────────┐  (?bat=NN&rssi=NN)       ┌────────────┐
  │ Raspberry Pi │ ◄──────────────────────── │  ESP32-E   │
  │  Go server   │ ──► 48000-byte 1-bit ──► │ Waveshare  │
  └──────────────┘     bitmap               │  7.5" EPD  │
                                             └────────────┘
```

## Commands

All commands run from `server/` unless noted.

```bash
# Build
go build ./cmd/server                           # local binary
GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=0 \
  go build -o calendar-server-armv7 ./cmd/server  # Pi cross-compile

# Lint
golangci-lint run                               # strict default: all config

# Test
go test ./...                                   # must pass before any server/ change is considered done

# Preview (run locally, visit http://localhost:8080/calendar.png)
./calendar-server -ical-url <your-secret-ical-url> -listen :8080
# or via env var (deploy path — avoids the URL appearing in `ps`):
ICAL_URL=<your-secret-ical-url> ./calendar-server -listen :8080

# Release (primary): trigger the "Release" workflow in GitHub Actions
#   Actions → Release → Run workflow → choose patch | minor | major
#   The workflow computes the next version, creates the tag, and runs goreleaser.

# Release (manual fallback, from repo root; requires git tag + GITHUB_TOKEN):
goreleaser release --snapshot --clean           # dry-run
goreleaser release --clean                      # real release
```

## Definition of done (server/)

Any change under `server/` is only complete when both of the following pass from `server/`:

```bash
go test ./...
golangci-lint run
```

- Run both before reporting the task done — don't rely on CI to catch failures.
- CI (`.github/workflows/ci.yml`) re-runs both checks on every push and PR. Treat a failing CI run as a blocker, but don't substitute it for the local check.
- New behavior requires a new or updated test in `internal/calendar/*_test.go`.
- Fix lint findings in place; do not silence them with `//nolint` unless the existing file already does so for the same reason.
- If a test or lint check is genuinely infeasible to satisfy (e.g., needs network, hardware), say so explicitly rather than skipping it silently.

## Server package structure

`internal/calendar` is the only package; it exports two things: `Config`, `Run`. All other types and functions are package-private.

| File | Responsibility |
|---|---|
| `server.go` | `Config`, `Run`, `server` struct, HTTP handlers, refresh loop |
| `fetch.go` | `event` type, `fetchTimeout` |
| `fetch_ical.go` | iCal HTTP fetch, parse, and event filtering |
| `render.go` | `buildDisplayData`, `renderImage`, DejaVu font embedding |
| `icons.go` | WiFi bar and battery icon primitives |
| `pack.go` | `pack1Bit`: RGBA → 1-bit MSB-first 48000-byte buffer |

## Test layout

Tests live in `internal/calendar/*_test.go`. All test files use
`package calendar_test` (blackbox); access to unexported symbols goes
through `export_test.go`, which stays in `package calendar` and re-exports
them under capitalized names.

When you need to test a new unexported function, add a line to
`export_test.go`:

```go
var NewSymbol = newSymbol
```

then reference `calendar.NewSymbol` from the `_test.go` file. Don't switch
a test file to `package calendar` — keep the blackbox boundary intact.

## Non-obvious invariants

- **Bitmap size is a hard protocol contract.** 800×480 = 48000 bytes. If you change `imgW`/`imgH` in `render.go`, you must also update `IMG_W`/`IMG_H` in the `.ino` and reflash the firmware. There is no version handshake — a size mismatch causes the ESP32 to skip the refresh.
- **Pack convention is paired.** `pack1Bit` writes MSB-first, bit=1=white. The firmware reads it with `drawInvertedBitmap(..., GxEPD_BLACK)`, which paints black where the bit is 0. If either side changes this convention, the image inverts.
- **Keep the export surface minimal.** `Config`, `Run` is intentional. Don't add exports unless `cmd/server` genuinely needs them.
- **Fonts panic on bad embed.** `render.go`'s `loadFonts` (`sync.OnceValue`) calls `truetype.Parse` on the embedded TTFs and panics on failure; `Run` calls it before the first fetch so this happens at startup. Don't remove the embedded font files.
- **Past-event cutoff:** timed events starting more than 30 minutes ago are hidden unless their `End` is still in the future. The constant is `now.Add(-30 * time.Minute)` in `render.go:buildDisplayData`.
- **Multi-day timed events** appear on every day they cover (`timedSpan`); on continuation days `onDay` shows them as all-day so the start time isn't repeated. An event ending exactly at midnight doesn't cover the next day.
- **Cancelled events** (`STATUS:CANCELLED`) are dropped in `eventsFromCal`, but still register as RECURRENCE-ID overrides so the base-series slot stays suppressed.
- **Startup is fail-fast.** `Run` validates timezone (non-empty, loadable) and `FetchInterval` (> 0), checks that `ICalURL` is non-empty (error: `ical URL required: set ICAL_URL env var or -ical-url flag`), then performs an initial synchronous calendar fetch; any misconfiguration fails immediately rather than serving a stale image.
- **iCal URL is a bearer token.** In production, supply it via the `ICAL_URL` env var (sourced from a `chmod 600` `EnvironmentFile` in the systemd unit) — **not** as a `-ical-url` flag, which would be visible in `ps`. The flag is fine for local dev. Fetch errors go through `redactURL` so the URL never reaches logs or `/healthz`.
- **Staleness threshold.** Data is stale after `staleAfterIntervals` (3) × `FetchInterval` without a successful fetch: `/healthz` returns 503 and the footer shows "(stale)". The footer's "Updated HH:MM" is the fetch time, not the render time.
- **Device check-in is informational.** `handleBin` records the last request time and battery; `/healthz` prints them as `device_last_seen_age` / `device_battery` but they never change its status code.
- **Request logging never logs the query string.** `requestLogLine` logs method, path, status, duration, and only the `bat`/`rssi` params, quoted — `?token=` must not reach the journal.
- **Routes are GET-only.** `routes()` uses `GET /path` patterns (HEAD matches too); other methods get 405 before any render.
- **Recurrences expand in the event's own zone.** `parseIcalDatetime` keeps the TZID/UTC zone so RRULE follows that zone's DST; `eventsFromCal` converts every event to the configured zone via `inLoc`. golang-ical parses floating and DATE values of EXDATE/RDATE/RECURRENCE-ID in `time.Local`; `anchorFloating` re-reads them in the configured zone. All-day occurrence ends are computed in whole days, not hours, so DST days don't shorten them.
- **Week Ahead fits by pixels.** `summarizeDay` takes a fit predicate; production passes `weekSummaryFits` (measures in the summary face, takes `renderMu`), and events that don't fit are counted in "+ N more".
- **Optional token auth.** When `AuthToken` (`AUTH_TOKEN` env / `-auth-token`) is set, `requireToken` rejects `/calendar.*` requests lacking `Authorization: Bearer <token>` or `?token=<token>` with 401 before rendering. `/healthz` stays open. With no token (the default) every endpoint is open to the LAN. The firmware sends `AUTH_TOKEN` from `secrets.h` (defaults to empty for older `secrets.h` files).
- **Server owns the wake schedule.** `handleBin` sets `X-Sleep-Seconds` (`sleepSeconds`: next :00/:30 in the server timezone, skipping marks closer than `wakeGuard`). The firmware has no NTP; it falls back to 30 min if the header is missing or out of range.
- **Font faces are cached and not goroutine-safe.** `face()` returns shared faces from `loadFonts()`; `renderImage` holds `fontSet.renderMu` for the whole render. Don't call `face()` outside a render.
- **Firmware contract is tested.** `protocol_test.go` reads the `.ino` and checks `IMG_W`/`IMG_H`, the query params, `drawInvertedBitmap(..., GxEPD_BLACK)`, `X-Sleep-Seconds` and `Authorization`. Update both sides together.

## Linter notes (`server/.golangci.yml`)

- `default: all` — every linter is on unless explicitly disabled.
- `exhaustruct` is disabled — too much churn from third-party struct literals (`http.Server`, `truetype.Options`) and from its `v5` rename breaking config compatibility.
- `tagliatelle` requires snake_case JSON tags.
- `_test.go` files relax `funlen`, `maintidx`, `err113`, `gosmopolitan`, `gochecknoglobals`, and gosec G101/G117/G306/G703.
- Non-test exclusions: `gochecknoglobals` for `loadFonts`, gosec G706 in `server.go`, and `gosmopolitan` `time.Local` in `fetch_ical.go` (`anchorFloating`).
- `formatters:` enables `gofmt` and `goimports` — in golangci v2 these are separate from `linters.default: all`.
- `gomodguard` is disabled only because it is deprecated; `gomodguard_v2` still runs.

## Firmware (`firmware/firebeetle_calendar/firebeetle_calendar.ino`)

Arduino sketch; built via Arduino IDE (not `go` or `make`). Before flashing, copy `firmware/firebeetle_calendar/secrets.h.example` → `firmware/firebeetle_calendar/secrets.h` and fill in `WIFI_SSID`, `WIFI_PASS`, `SERVER_HOST`. `SERVER_PORT` is set directly in the `USER CONFIG` block of the `.ino`; `EPD_PWR` (GPIO wired to the HAT's PWR pin, `-1` = tied to 3V3) is next to the pin map. The image buffer is a static `imgBuf`. The firmware omits `bat` when the reading is under `NO_BATT_SENSE_MV` (no sense divider), and counts the sleep from when the response arrived, so draw time comes out of it. `secrets.h` is gitignored; `.claude/settings.json` also blocks Claude from reading it. Battery voltage is read from GPIO34 through a 1:2 internal divider; calibration lives in `batteryPercent()`.
