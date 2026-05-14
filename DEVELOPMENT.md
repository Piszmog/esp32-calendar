# Development

How to build, test, and extend the server code. For deployment steps, see [README.md](README.md).

## Repo layout

```
esp32-calendar/
├── server/                     Go server (runs on the Pi)
│   ├── go.mod
│   ├── cmd/server/main.go      Flags, entrypoint
│   └── internal/calendar/      The only package
│       ├── server.go           Config, Run, HTTP handlers, refresh loop
│       ├── auth.go             RunAuth, OAuth token storage, tokenSource
│       ├── fetch.go            Google Calendar API client, event type, fetchEvents
│       ├── render.go           buildDisplayData, renderImage, DejaVu font embedding
│       ├── icons.go            WiFi bar and battery icon drawing
│       ├── pack.go             pack1Bit: RGBA → 1-bit MSB-first 48000-byte buffer
│       └── fonts/              Embedded TTFs (DejaVu Sans regular + bold)
├── firmware/
│   └── firebeetle_calendar/
│       └── firebeetle_calendar.ino   ESP32 sketch
├── deploy/
│   └── calendar.service        systemd unit for the Pi
├── .goreleaser.yaml            Cross-compiled release config
└── CLAUDE.md                   Agent-side invariants (source of truth for Claude)
```

The package exports exactly three things: `Config`, `Run`, `RunAuth`. Everything
else is package-private.

## Commands

All commands run from `server/` unless noted.

```bash
# Build (local, for your host OS)
go build ./cmd/server

# Cross-compile for the Pi (ARMv7, static binary, ~15 MB)
GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=0 \
  go build -o calendar-server-armv7 ./cmd/server

# Release (primary): trigger the "Release" workflow in GitHub Actions
#   Actions → Release → Run workflow → choose patch | minor | major
#   See "Cutting a release" section below.

# Release (manual fallback, from repo root; requires git tag + GITHUB_TOKEN):
goreleaser release --snapshot --clean    # dry-run, no tag, no upload
goreleaser release --clean               # real release

# Lint
golangci-lint run

# Test
go test ./...

# Preview (run locally, then visit http://localhost:8080/calendar.png)
./calendar-server -listen :8080

# OAuth flow (first-time setup — requires SSH tunnel: ssh -L 8090:127.0.0.1:8090 server@esp32-calendar.local)
./calendar-server -auth -tz America/Los_Angeles
```

## Definition of done

Any change under `server/` is only complete when both pass:

```bash
go test ./...
golangci-lint run
```

New behavior requires a new or updated test in `internal/calendar/*_test.go`. Fix
lint findings in place — don't add `//nolint` unless the existing file already
does so for the same reason. If a check is genuinely infeasible (e.g., requires
network or hardware), say so explicitly rather than skipping it silently.

## Test layout

Tests live in `internal/calendar/*_test.go`. All test files use
`package calendar_test` (blackbox). Access to unexported symbols goes through
`export_test.go`, which stays in `package calendar` and re-exports them under
capitalized names.

When adding a test for a new unexported function, add a line to `export_test.go`:

```go
var NewSymbol = newSymbol
```

Then reference `calendar.NewSymbol` from the `_test.go` file. Don't switch a test
file to `package calendar` — keep the blackbox boundary intact. Tests use
`stretchr/testify` (already in `go.mod`).

## Non-obvious invariants

**Bitmap size is a hard protocol contract.** `800×480 / 8 = 48000 bytes`. If you
change `imgW`/`imgH` in `render.go`, you must also update `IMG_W`/`IMG_H` in
`firmware/firebeetle_calendar/firebeetle_calendar.ino` and reflash. There is no
version handshake — a size mismatch causes the ESP32 to silently skip the refresh.

**Pack convention is paired.** `pack1Bit` writes MSB-first, bit=1=white. The
firmware reads with `drawInvertedBitmap(..., GxEPD_BLACK)`, which paints black
where the bit is 0. If either side changes this convention, the image inverts.

**Font `init()` panics on bad embed.** `render.go`'s `init()` calls
`truetype.Parse` on the embedded TTFs and panics on failure. Don't remove the
embedded font files under `internal/calendar/fonts/`.

**Past-event cutoff.** Events starting more than 30 minutes ago are hidden. The
constant is `now.Add(-30 * time.Minute)` in `render.go:buildDisplayData`.

**Startup is fail-fast.** `Run` validates the timezone and performs an initial
synchronous calendar fetch; a misconfiguration fails immediately rather than
serving a stale image.

**HTTP endpoints are unauthenticated.** The default `-listen :8080` binds to all
interfaces — anyone on the LAN can fetch `/calendar.bin` (which contains event
titles) or the PNG preview. Bind to `127.0.0.1:8080` and front with a reverse
proxy if this is a concern.

**Export surface is intentionally minimal.** `Config`, `Run`, `RunAuth` is the
full public API. Don't add exports unless `cmd/server` genuinely needs them.

## Cutting a release

Releases are dispatched manually from GitHub Actions:

1. Make sure `main` is green in CI.
2. **Actions → Release → Run workflow** → pick `patch` / `minor` / `major`.
3. The workflow reads the latest release tag, bumps the version, creates the
   tag, and runs `goreleaser release --clean`.
4. Confirm the new release at `https://github.com/<owner>/<repo>/releases/latest`
   has the expected tarballs (Linux/Darwin × amd64/arm64/armv7, minus the
   unsupported darwin-armv7 combination).

If GitHub Actions is unavailable, fall back to a local release:

```bash
git tag v0.x.y && git push --tags
GITHUB_TOKEN=ghp_... goreleaser release --clean    # from repo root
```

## Linter notes

`server/.golangci.yml` runs with `default: all` — every linter is on unless
explicitly disabled.

- **`exhaustruct`** — struct literals must fill all fields. Exceptions:
  `net/http.Cookie`, `net/http.Server`, `log/slog.HandlerOptions`.
- **`tagliatelle`** — requires snake_case JSON tags.
- **`_test.go` files** relax `funlen`, `maintidx`, `exhaustruct`, and `err113`.

## Coordinated protocol changes

Some constants in the `.ino` are a hard contract with the server:

- **Buffer size:** `IMG_W × IMG_H / 8 = 48000`. Changing `imgW`/`imgH` in
  `render.go` and `IMG_W`/`IMG_H` in the `.ino` must land in the same deploy.
- **Pack convention:** `pack1Bit` MSB-first, bit=1=white; firmware reads with
  `drawInvertedBitmap(..., GxEPD_BLACK)`. Flipping either side inverts the image.
- **Query params:** the ESP32 sends `?bat=NN&rssi=NN`; the server renders these
  into the status bar. Renaming a param requires changes on both sides.

When a change touches any of the above, deploy in this order:

1. Build and stage the new server binary (`scp` to `calendar-server.new`) but
   **do not restart yet**.
2. Flash all ESP32 boards (section below or Arduino IDE).
3. Swap and restart the server (rename `.new` → `calendar-server`,
   `systemctl restart calendar`).
4. Verify the buffer size:

```bash
curl -sI "http://esp32-calendar.local:8080/calendar.bin?bat=80&rssi=70" | grep -i content-length
# Expect: Content-Length: 48000
```

## arduino-cli (scripted firmware flashing)

Useful when iterating on firmware without the IDE.

**One-time setup:**

```bash
brew install arduino-cli
arduino-cli config init
arduino-cli core update-index
arduino-cli core install esp32:esp32
arduino-cli lib install "GxEPD2" "Adafruit GFX Library"
```

**Discover the correct FQBN — do not assume:**

```bash
arduino-cli board listall dfrobot
```

The expected entry is `esp32:esp32:dfrobot_firebeetle2_esp32e`. If it doesn't
appear, fall back to `esp32:esp32:esp32` (ESP32 Dev Module).

**Compile, upload, monitor:**

```bash
arduino-cli board list
# Note the /dev/cu.* path for the FireBeetle

FQBN=esp32:esp32:dfrobot_firebeetle2_esp32e
PORT=/dev/cu.SLAB_USBtoUART   # or /dev/cu.usbserial-*

arduino-cli compile --fqbn $FQBN firmware/firebeetle_calendar/
arduino-cli upload  --fqbn $FQBN -p $PORT firmware/firebeetle_calendar/
arduino-cli monitor -p $PORT -c baudrate=115200
```

**macOS:** The CP2102 driver may need a one-time approval under **System Settings
→ Privacy & Security** after first plug-in.

## Firmware rollback

Re-flash from a known-good commit:

```bash
git log -- firmware/           # find the last good commit hash
git checkout <good-ref> -- firmware/
# flash via Arduino IDE or arduino-cli
git checkout HEAD -- firmware/ # restore working tree after flashing
```
