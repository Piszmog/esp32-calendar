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
| `fetch.go` | `event` type, `fetchEvents` dispatcher |
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
- **Font `init()` panics on bad embed.** `render.go`'s `init()` calls `truetype.Parse` on the embedded TTFs and panics on failure. Don't remove the embedded font files.
- **Past-event cutoff:** events starting more than 30 minutes ago are hidden. The constant is `now.Add(-30 * time.Minute)` in `render.go:buildDisplayData`.
- **Startup is fail-fast.** `Run` validates timezone and performs an initial synchronous calendar fetch; a misconfiguration fails immediately rather than serving a stale image.
- **No authentication on HTTP endpoints.** The default `-listen :8080` binds to all interfaces; anyone on the LAN can fetch `/calendar.bin` (which contains event titles) or the PNG preview. If this is a concern, bind to `127.0.0.1:8080` and front with a reverse proxy, or restrict firewall rules.

## Linter notes (`server/.golangci.yml`)

- `default: all` — every linter is on unless explicitly disabled.
- `exhaustruct` is enabled — struct literals must fill all fields. Exceptions: `net/http.Cookie`, `net/http.Server`, `log/slog.HandlerOptions`.
- `tagliatelle` requires snake_case JSON tags.
- `_test.go` files relax `funlen`, `maintidx`, `exhaustruct`, and `err113`.

## Firmware (`firmware/firebeetle_calendar/firebeetle_calendar.ino`)

Arduino sketch; built via Arduino IDE (not `go` or `make`). Before flashing, copy `firmware/firebeetle_calendar/secrets.h.example` → `firmware/firebeetle_calendar/secrets.h` and fill in `WIFI_SSID`, `WIFI_PASS`, `SERVER_HOST`. `SERVER_PORT` and `SLEEP_MINUTES` are set directly in the `USER CONFIG` block of the `.ino`. `secrets.h` is gitignored; `.claude/settings.json` also blocks Claude from reading it. Battery voltage is read from GPIO34 through a 1:2 internal divider; calibration lives in `batteryPercent()`.
