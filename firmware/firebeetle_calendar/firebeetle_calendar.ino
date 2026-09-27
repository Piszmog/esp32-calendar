/*
 * FireBeetle 2 ESP32-E + Waveshare 7.5" e-paper calendar client
 *
 * Wakes from deep sleep aligned to :00/:30 wall-clock marks, downloads a packed 1-bit
 * 800x480 bitmap from the calendar server (with current battery% and WiFi RSSI
 * as query params so the server can render them into the status bar),
 * pushes it to the display, sleeps.
 *
 * Required libraries (Arduino Library Manager):
 *   - GxEPD2 (Jean-Marc Zingg)
 *   - Adafruit GFX
 *
 * Board: "FireBeetle-ESP32" or "DFRobot FireBeetle 2 ESP32-E".
 *
 * Wiring (Waveshare 7.5" e-paper HAT -> FireBeetle 2 ESP32-E):
 *   VCC   -> 3V3
 *   GND   -> GND
 *   DIN   -> GPIO 23  (MOSI)
 *   CLK   -> GPIO 18  (SCK)
 *   CS    -> GPIO 13  (D7)
 *   DC    -> GPIO 22  (SCL)
 *   RST   -> GPIO 21  (SDA)
 *   BUSY  -> GPIO 14  (D6)
 *   PWR   -> 3V3  (rev 2.3 HAT only — older rev 2.2 has no PWR pin.
 *                  For max power savings, connect to a free GPIO instead
 *                  and pull LOW before deep sleep to fully cut display power.)
 */

#include <WiFi.h>
#include <HTTPClient.h>
#include <GxEPD2_BW.h>
#include "esp_sleep.h"
#include "esp_task_wdt.h"
#include "driver/gpio.h"
#include <time.h>

// ============ USER CONFIG ============
#include "secrets.h"
const uint16_t SERVER_PORT = 8080;
// =====================================

// Pin map
#define EPD_CS    13
#define EPD_DC    22
#define EPD_RST   21
#define EPD_BUSY  14

// FireBeetle 2 ESP32-E battery sensing
// On the FireBeetle 2 ESP32-E the battery is monitored via GPIO34
// (an input-only ADC pin) through an internal voltage divider.
#define BATT_ADC_PIN  34

// Waveshare 7.5" 800x480 B/W — GDEY075T7, UC8179 controller.
GxEPD2_BW<GxEPD2_750_T7, GxEPD2_750_T7::HEIGHT> display(
    GxEPD2_750_T7(EPD_CS, EPD_DC, EPD_RST, EPD_BUSY));

constexpr uint32_t IMG_W = 800;
constexpr uint32_t IMG_H = 480;
constexpr uint32_t BUF_BYTES = IMG_W * IMG_H / 8;   // 48000

// Whole wake cycle must finish within this, or the task watchdog resets the
// board (worst case: 28 s WiFi + 2 s NTP + 20 s fetch + ~20 s display).
constexpr uint32_t WDT_TIMEOUT_MS = 120000;

// Single deadline for the HTTP fetch, from request start to last byte.
constexpr uint32_t FETCH_DEADLINE_MS = 20000;

// Below LOW_BATT_MV the board shows a "charge me" screen and sleeps until
// reset, instead of browning out on every wake. Readings under
// NO_BATT_SENSE_MV mean the battery-sense divider is absent (some clones),
// so the cutoff is skipped rather than bricking the board.
constexpr uint32_t LOW_BATT_MV      = 3400;
constexpr uint32_t NO_BATT_SENSE_MV = 2500;

// Failed wakes back off 5 -> 15 -> 30 -> 60 min. The error screen replaces the
// calendar only after ERROR_SCREEN_AFTER consecutive failures, and is drawn
// once, not on every retry.
constexpr uint8_t ERROR_SCREEN_AFTER = 3;

// Survive deep sleep (cleared on power-on or reset).
RTC_DATA_ATTR uint8_t rtcFailCount   = 0;
RTC_DATA_ATTR bool    rtcErrorShown  = false;
RTC_DATA_ATTR uint8_t rtcBssid[6]    = {0};
RTC_DATA_ATTR int32_t rtcChannel     = 0;   // 0 = no cached AP

void goToSleep(uint64_t seconds) {
    Serial.flush();
    esp_sleep_enable_timer_wakeup(seconds * 1000000ULL);
    esp_deep_sleep_start();
}

// Returns seconds until the next :00 or :30 wall-clock mark.
// Falls back to 30 min if NTP hasn't synced (time < 2024-01-01).
// Guard: if we're within 60s of a mark, push to the following one to
// avoid a near-zero sleep after a slow fetch+render cycle.
uint64_t nextWakeSeconds() {
    time_t now = time(nullptr);
    if (now < 1704067200) return 30ULL * 60ULL;
    struct tm t;
    gmtime_r(&now, &t);
    int secsPastMark = (t.tm_min % 30) * 60 + t.tm_sec;
    int secsToMark   = (30 * 60) - secsPastMark;
    if (secsToMark < 60) secsToMark += 30 * 60;
    return (uint64_t)secsToMark;
}

// Returns seconds to sleep after the given number of consecutive failures.
uint64_t backoffSeconds(uint8_t failures) {
    switch (failures) {
        case 0:
        case 1:  return 5ULL * 60ULL;
        case 2:  return 15ULL * 60ULL;
        case 3:  return 30ULL * 60ULL;
        default: return 60ULL * 60ULL;
    }
}

bool waitForWiFi(uint32_t timeoutMs) {
    uint32_t t0 = millis();
    while (WiFi.status() != WL_CONNECTED && millis() - t0 < timeoutMs) {
        delay(250);
        Serial.print('.');
    }
    Serial.println();
    return WiFi.status() == WL_CONNECTED;
}

// Connects using the AP (BSSID + channel) cached from the last wake, which
// skips the scan; falls back to a full scan if that fails.
bool connectWiFi() {
    WiFi.mode(WIFI_STA);
    if (rtcChannel > 0) {
        WiFi.begin(WIFI_SSID, WIFI_PASS, rtcChannel, rtcBssid);
        if (waitForWiFi(8000)) return true;
        Serial.println("cached AP failed, scanning");
        WiFi.disconnect();
        rtcChannel = 0;
    }
    WiFi.begin(WIFI_SSID, WIFI_PASS);
    if (!waitForWiFi(20000)) return false;
    memcpy(rtcBssid, WiFi.BSSID(), sizeof(rtcBssid));
    rtcChannel = WiFi.channel();
    return true;
}

const char* wifiStatusStr(wl_status_t status) {
    switch (status) {
        case WL_NO_SSID_AVAIL:   return "SSID not found";
        case WL_CONNECT_FAILED:  return "auth failed (check password)";
        case WL_CONNECTION_LOST: return "connection lost";
        case WL_DISCONNECTED:    return "disconnected";
        default:                 return "connect timed out";
    }
}

// Read battery voltage on FireBeetle 2 ESP32-E (GPIO34, 1:2 divider).
// Returns voltage in millivolts.
uint32_t readBatteryMv() {
    // Discard first two reads: ESP32 SAR ADC produces a noisier sample
    // on the first call after deep-sleep wake or cold boot.
    analogReadMilliVolts(BATT_ADC_PIN);
    analogReadMilliVolts(BATT_ADC_PIN);

    // Average a few calibrated reads to smooth noise.
    uint32_t pin_mv = 0;
    const int N = 16;
    for (int i = 0; i < N; i++) {
        pin_mv += analogReadMilliVolts(BATT_ADC_PIN);
        delay(2);
    }
    pin_mv /= N;

    // FireBeetle 2 has a 1:2 internal divider on the battery sense pin.
    return pin_mv * 2;
}

// Map battery voltage (mV) to percentage (0-100) using a simple LiPo curve.
int batteryPercent(uint32_t mv) {
    if (mv >= 4100) return 100;
    if (mv >= 3950) return 75 + (mv - 3950) * 25 / 150;
    if (mv >= 3800) return 50 + (mv - 3800) * 25 / 150;
    if (mv >= 3700) return 25 + (mv - 3700) * 25 / 100;
    if (mv >= 3500) return     (mv - 3500) * 25 / 200;
    return 0;
}

bool fetchImage(uint8_t* buf, int batPct, int rssi, char* reason, size_t reasonLen) {
    char url[160];
    snprintf(url, sizeof(url),
             "http://%s:%u/calendar.bin?bat=%d&rssi=%d",
             SERVER_HOST, SERVER_PORT, batPct, rssi);

    Serial.printf("GET %s\n", url);

    uint32_t t0 = millis();
    HTTPClient http;
    http.setTimeout(FETCH_DEADLINE_MS);
    if (!http.begin(url)) {
        snprintf(reason, reasonLen, "http.begin() failed");
        Serial.println(reason);
        return false;
    }

    int code = http.GET();
    if (code != HTTP_CODE_OK) {
        if (code > 0) {
            snprintf(reason, reasonLen, "HTTP %d", code);
        } else {
            snprintf(reason, reasonLen, "%s", http.errorToString(code).c_str());
        }
        Serial.println(reason);
        http.end();
        return false;
    }
    int len = http.getSize();
    if (len != (int)BUF_BYTES) {
        snprintf(reason, reasonLen, "size %d, expected %u", len, BUF_BYTES);
        Serial.println(reason);
        http.end();
        return false;
    }

    WiFiClient* s = http.getStreamPtr();
    if (!s) {
        snprintf(reason, reasonLen, "no stream");
        Serial.println(reason);
        http.end();
        return false;
    }
    uint32_t got = 0;
    while (got < BUF_BYTES && millis() - t0 < FETCH_DEADLINE_MS) {
        size_t avail = s->available();
        if (avail) {
            int n = s->readBytes(buf + got,
                                 min(avail, (size_t)(BUF_BYTES - got)));
            got += n;
        } else {
            delay(1);
        }
    }
    http.end();
    if (got != BUF_BYTES) {
        snprintf(reason, reasonLen, "read %u/%u bytes (timeout)", got, BUF_BYTES);
    }
    Serial.printf("read %u/%u bytes\n", got, BUF_BYTES);
    return got == BUF_BYTES;
}

void drawBuffer(const uint8_t* buf) {
    display.init(115200, false, 2, false);
    display.setRotation(0);
    display.setFullWindow();
    display.firstPage();
    do {
        display.fillScreen(GxEPD_WHITE);
        // Server outputs MSB-first packed 1-bit, bit=1 white, bit=0 black.
        // drawInvertedBitmap paints the supplied color where the bit is 0.
        display.drawInvertedBitmap(0, 0, buf, IMG_W, IMG_H, GxEPD_BLACK);
    } while (display.nextPage());
    display.hibernate();
}

void drawError(const char* title, const char* detail, const char* statusLine) {
    display.init(115200, false, 2, false);
    display.setRotation(0);
    display.setFullWindow();
    display.firstPage();
    do {
        display.fillScreen(GxEPD_WHITE);
        display.setTextColor(GxEPD_BLACK);
        display.setTextSize(3);
        display.setCursor(20, 60);
        display.print(title);
        display.setTextSize(2);
        display.setCursor(20, 140);
        display.print(detail);
        display.setCursor(20, 420);
        display.print(statusLine);
    } while (display.nextPage());
    display.hibernate();
}

// Records a failed wake, shows the error screen once the failure streak
// reaches ERROR_SCREEN_AFTER (leaving the last calendar up until then), and
// sleeps with backoff. Does not return.
void failAndSleep(const char* title, const char* detail, const char* statusLine) {
    if (rtcFailCount < UINT8_MAX) rtcFailCount++;
    Serial.printf("failure %u: %s: %s\n", rtcFailCount, title, detail);
    if (rtcFailCount >= ERROR_SCREEN_AFTER && !rtcErrorShown) {
        drawError(title, detail, statusLine);
        rtcErrorShown = true;
    }
    WiFi.disconnect(true);
    WiFi.mode(WIFI_OFF);
    uint64_t sleepSecs = backoffSeconds(rtcFailCount);
    Serial.printf("sleeping %llus\n", sleepSecs);
    goToSleep(sleepSecs);
}

void setup() {
    Serial.begin(115200);
    delay(100);
    Serial.println("\n== calendar wake ==");

    // The core starts the task watchdog but doesn't watch this task; extend
    // the timeout to cover a whole cycle and subscribe, so a hang resets.
    esp_task_wdt_config_t wdtConfig = {
        .timeout_ms = WDT_TIMEOUT_MS,
        .idle_core_mask = 0,
        .trigger_panic = true,
    };
    esp_task_wdt_reconfigure(&wdtConfig);
    esp_task_wdt_add(NULL);

    // Read battery BEFORE WiFi powers up (cleaner reading).
    uint32_t mv = readBatteryMv();
    int batPct = batteryPercent(mv);
    Serial.printf("battery: %u mV (%d%%)\n", mv, batPct);

    if (mv > NO_BATT_SENSE_MV && mv < LOW_BATT_MV) {
        Serial.println("battery low — sleeping until reset");
        char detail[48];
        snprintf(detail, sizeof(detail), "%u mV. Charge, then press reset.", mv);
        drawError("Battery low", detail, "");
        Serial.flush();
        esp_deep_sleep_start();   // no wakeup source: sleeps until reset
    }

    char status[40];
    snprintf(status, sizeof(status), "battery %d%%", batPct);
    if (!connectWiFi()) {
        failAndSleep("WiFi connect failed", wifiStatusStr(WiFi.status()), status);
    }
    int rssi = WiFi.RSSI();
    Serial.printf("rssi: %d dBm\n", rssi);
    snprintf(status, sizeof(status), "battery %d%%  rssi %d dBm", batPct, rssi);

    configTime(0, 0, "pool.ntp.org", "time.google.com");
    for (int i = 0; i < 20 && time(nullptr) < 1704067200; i++) delay(100);
    if (time(nullptr) < 1704067200) {
        Serial.println("warning: NTP not synced, next wake not aligned to :00/:30");
    }

    uint8_t* buf = (uint8_t*)malloc(BUF_BYTES);
    if (!buf) {
        failAndSleep("Out of memory", "malloc failed", status);
    }

    char reason[64];
    if (!fetchImage(buf, batPct, rssi, reason, sizeof(reason))) {
        free(buf);
        failAndSleep("Calendar fetch failed", reason, status);
    }
    drawBuffer(buf);
    free(buf);

    rtcFailCount = 0;
    rtcErrorShown = false;
    WiFi.disconnect(true);
    WiFi.mode(WIFI_OFF);
    uint64_t sleepSecs = nextWakeSeconds();
    Serial.printf("sleeping %llus\n", sleepSecs);
    goToSleep(sleepSecs);
}

void loop() {}
