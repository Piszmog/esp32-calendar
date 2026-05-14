# E-paper calendar display

A Google Calendar display on a Waveshare 7.5" e-paper, driven by a FireBeetle 2
ESP32-E that fetches a pre-rendered bitmap from a small Go server on a Raspberry Pi.

![Rendered calendar display](docs/demo.png)

## How it works

```
                              HTTP fetch every 30 min
       ┌──────────────┐       (bat%, RSSI in query)         ┌────────────┐
       │ Raspberry Pi │ ◄────────────────────────────────── │ FireBeetle │
       │  Go server   │ ──────► 48000-byte 1-bit bitmap ──► │  ESP32-E   │
       │              │                                     │            │
       │ Google Cal   │                                     │ Waveshare  │
       │  + OAuth     │                                     │ 7.5" EPD   │
       └──────────────┘                                     └────────────┘
```

## Hardware

- DFRobot FireBeetle 2 ESP32-E
- Waveshare 7.5" V2 e-paper HAT (800×480 B/W, GDEY075T7)
- Raspberry Pi (any model; Pi 2 v1.1 or later works)
- LiPo battery (2000–5000 mAh)
- USB cable for flashing the ESP32
- Jumper wires

## 1. Google Cloud setup (once)

1. Go to <https://console.cloud.google.com/> and create a project.
2. **APIs & Services → Enable APIs** → enable **Google Calendar API**.
3. **OAuth consent screen** → External → fill in app name + your email.
   Add your own Google account to **Test users**.
4. **Credentials → Create Credentials → OAuth client ID** → **Desktop app**.
5. After creation, **edit the client** and add this redirect URI exactly:
   ```
   http://127.0.0.1:8090/callback
   ```
6. Download the JSON; rename it `credentials.json`.

## 2. Wire the display

Waveshare 7.5" V2 e-paper HAT → FireBeetle 2 ESP32-E:

| e-paper | FireBeetle GPIO  |
|---------|------------------|
| VCC     | 3V3              |
| GND     | GND              |
| DIN     | 23 (MOSI)        |
| CLK     | 18 (SCK)         |
| CS      | 13 (D7)          |
| DC      | 22 (SCL)         |
| RST     | 21 (SDA)         |
| BUSY    | 14 (D6)          |
| PWR     | 3V3              |

The PWR pin only exists on the **rev 2.3** Driver HAT. Older rev 2.2 HATs don't
have it — skip that row. For extra battery savings you can connect PWR to a spare
GPIO instead of 3V3 and pull it LOW before `esp_deep_sleep_start()` to fully cut
display power between refreshes.

## 3. Build the server

**Option A — Download (recommended).** Grab the prebuilt ARMv7 binary from
[GitHub Releases](https://github.com/<owner>/<repo>/releases/latest):

```bash
curl -L -o calendar-display.tar.gz \
  https://github.com/<owner>/<repo>/releases/latest/download/calendar-display_Linux_armv7.tar.gz
tar -xzf calendar-display.tar.gz
```

The tarball contains `calendar-server`, `README.md`,
`firmware/firebeetle_calendar/firebeetle_calendar.ino`,
`firmware/firebeetle_calendar/secrets.h.example`, and
`deploy/calendar.service`.

> **Note:** replace `<owner>/<repo>` with the actual GitHub path once the repo
> is published.

**Option B — Build locally.** Install
[goreleaser](https://goreleaser.com/install/), then from the repo root:

```bash
goreleaser release --snapshot --clean
```

The ARMv7 binary lands at
`goreleaser-dist/calendar-display_Linux_armv7/calendar-server`. Use this
when iterating on the server code; for normal deploys, Option A is simpler.

**Option C — Build on the Pi.**

```bash
sudo apt install golang-go
cd server && go build -o calendar-server ./cmd/server
```

## 4. Deploy the server

### Copy files to the Pi

```bash
ssh server@esp32-calendar.local "mkdir -p ~/calendar"

scp goreleaser-dist/calendar-display_Linux_armv7/calendar-server \
    server@esp32-calendar.local:~/calendar/calendar-server

scp credentials.json        server@esp32-calendar.local:~/calendar/credentials.json
scp deploy/calendar.service server@esp32-calendar.local:~/calendar/calendar.service
```

### Run the one-time OAuth flow

The OAuth callback must reach the Pi, so open an SSH port-forward first:

```bash
ssh -L 8090:127.0.0.1:8090 server@esp32-calendar.local
```

In that SSH session:

```bash
cd ~/calendar
chmod +x calendar-server
./calendar-server -auth -tz America/Los_Angeles
```

It prints an authorization URL. Open it in your **laptop's browser** (the tunnel
handles the redirect). After approving:

```
Auth complete. Token written to token.json
```

`token.json` refreshes automatically — you shouldn't need to re-run this unless
you revoke the app's access in Google.

### Install the systemd unit

Edit `~/calendar/calendar.service` on the Pi if you need a different `-tz` or
`-fetch-interval`, then:

```bash
sudo cp ~/calendar/calendar.service /etc/systemd/system/calendar.service
sudo systemctl daemon-reload
sudo systemctl enable --now calendar.service
```

### Verify

```bash
journalctl -u calendar -f
```

You should see `calendar-server <version> starting` followed by a successful
calendar fetch. Then from your laptop:

```bash
curl http://esp32-calendar.local:8080/healthz
```

Open `http://esp32-calendar.local:8080/calendar.png` in a browser to confirm the
rendered image looks correct.

**Give the Pi a static DHCP reservation** in your router so the ESP32's hardcoded
server IP doesn't drift.

Endpoints:

| URL | Purpose |
|-----|---------|
| `http://<pi>:8080/calendar.png` | Preview in any browser |
| `http://<pi>:8080/calendar.bin` | Packed 1-bit bitmap the ESP32 fetches |
| `http://<pi>:8080/healthz`      | Plain-text health + last-fetch age |

## 5. Flash the firmware

**Arduino IDE setup (first time only):**

1. **Boards Manager** → install **esp32 by Espressif**.
2. **Library Manager** → install **GxEPD2** and **Adafruit GFX Library**.

**Every flash:**

1. Copy `firmware/firebeetle_calendar/secrets.h.example` →
   `firmware/firebeetle_calendar/secrets.h` and fill in:
   ```cpp
   #define WIFI_SSID  "your-network"
   #define WIFI_PASS  "your-password"
   #define SERVER_HOST "192.168.1.50"  // Pi's static IP
   ```
2. Open `firmware/firebeetle_calendar/firebeetle_calendar.ino`. Edit
   `SERVER_PORT` and `SLEEP_MINUTES` in the `USER CONFIG` block if needed.
3. **Tools → Board → DFRobot FireBeetle 2 ESP32-E** (or "ESP32 Dev Module").
4. Select the serial port and click **Upload**.

Watch the serial monitor at 115200 baud. On wake you should see:

```
== calendar wake ==
battery: 4012 mV (76%)
.....
rssi: -55 dBm
GET http://192.168.1.50:8080/calendar.bin?bat=76&rssi=-55
read 48000/48000 bytes
[display refreshes ~15 s later]
```

For scripted re-flashing with `arduino-cli`, see [DEVELOPMENT.md](DEVELOPMENT.md).

**macOS note:** The FireBeetle 2 ESP32-E uses the Silicon Labs CP2102 USB bridge.
On Apple Silicon you may need to approve it once under **System Settings → Privacy
& Security** after first plug-in.

## 6. Customizing

| What | Where |
|------|-------|
| Timezone | `-tz` flag in `deploy/calendar.service` (or pass it at the command line) |
| How often the server polls Google | `-fetch-interval` flag (default `10m`) |
| Which calendar | `-calendar <calendar-id>` (default `primary`; find IDs in Google Calendar settings → "Integrate calendar") |
| How often the display refreshes | `SLEEP_MINUTES` in the `.ino` `USER CONFIG` block |
| Past-event cutoff | `now.Add(-30 * time.Minute)` in `server/internal/calendar/render.go` |

To preview layout changes without flashing: run the server locally with
`./calendar-server -listen :8080` and open `http://localhost:8080/calendar.png`
in a browser.

## 7. Updating

### Deploy a new server version

```bash
# Download the latest release tarball
curl -L -o /tmp/calendar.tar.gz \
  https://github.com/<owner>/<repo>/releases/latest/download/calendar-display_Linux_armv7.tar.gz
tar -xzf /tmp/calendar.tar.gz -C /tmp/

# Stage to a temp name (never overwrite a running binary mid-transfer)
scp /tmp/calendar-server \
    server@esp32-calendar.local:~/calendar/calendar-server.new

# Atomic swap + restart
ssh server@esp32-calendar.local '
  cd ~/calendar &&
  mv calendar-server calendar-server.prev &&
  mv calendar-server.new calendar-server &&
  chmod +x calendar-server &&
  sudo systemctl restart calendar
'

# Verify
ssh server@esp32-calendar.local 'journalctl -u calendar -n 20 --no-pager'
curl -fsS http://esp32-calendar.local:8080/healthz
```

`calendar-server.prev` is kept as a one-cycle rollback target. To roll back:
swap `calendar-server` and `calendar-server.prev` and restart the service.

### Update the firmware

Re-flash via the Arduino IDE with the same steps as section 5. `secrets.h` is
gitignored and preserved across `git pull`.

## 8. Battery life

- Deep sleep current: ~10 µA
- Active draw during a refresh: ~120 mA for ~15 s
- 30-minute refresh interval → ~1 mAh/hour averaged

A 2000 mAh LiPo gets ~80 days between charges; a 5000 mAh battery gets 6+ months.

## Troubleshooting

| Symptom | First thing to check |
|---------|---------------------|
| OAuth `redirect_uri_mismatch` | The URI in Google Cloud must be exactly `http://127.0.0.1:8090/callback` — no `localhost`, no trailing slash |
| Service won't start | `journalctl -u calendar -n 50 --no-pager` |
| Server exits immediately | Bad `-tz` value or missing `credentials.json` — the server fails fast by design |
| `token expired, refresh failed` in logs | OAuth token revoked — re-run the OAuth flow (section 4) |
| Port 8080 unreachable from ESP32 | `sudo ufw status` on the Pi — allow port 8080 if a firewall is active |
| Service starts but no image | Check `journalctl -u calendar` for fetch errors; confirm `token.json` exists in `~/calendar/` |
| Serial shows WiFi failure / no IP | `WIFI_SSID` / `WIFI_PASS` in `firmware/firebeetle_calendar/secrets.h` |
| Serial shows HTTP 404 or connection refused | `SERVER_HOST` wrong, or service not running — `systemctl status calendar` on the Pi |
| ESP32 boots but display stays blank | Re-check wiring (section 2), or a pack/draw convention mismatch (see DEVELOPMENT.md) |
| Image is inverted | In the firmware, swap `drawInvertedBitmap` → `drawBitmap` |
| Battery reads 0% always | Confirm your FireBeetle has the battery sense voltage divider on GPIO34 (some clones omit it); adjust calibration in `batteryPercent()` in the `.ino` |
| Battery icon obviously wrong | Adjust the LiPo curve in `batteryPercent()` in the `.ino` |
| `arduino-cli` can't find the board | macOS CP2102 driver approval (see section 5); re-run `arduino-cli board list` |
