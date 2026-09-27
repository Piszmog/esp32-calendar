/*
 * FireBeetle 2 ESP32-E + Waveshare 7.5" e-paper calendar client
 *
 * Wakes from deep sleep aligned to :00/:30 wall-clock marks (the server says how
 * long to sleep via X-Sleep-Seconds), downloads a packed 1-bit
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

// ============ USER CONFIG ============
#include "secrets.h"
const uint16_t SERVER_PORT = 8080;
#ifndef AUTH_TOKEN
#define AUTH_TOKEN ""   // older secrets.h without a token: send none
#endif
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
// board (worst case: 28 s WiFi + 20 s fetch + ~20 s display).
constexpr uint32_t WDT_TIMEOUT_MS = 120000;

// Single deadline for the HTTP fetch, from request start to last byte.
constexpr uint32_t FETCH_DEADLINE_MS = 20000;

// Below LOW_BATT_MV the board shows a "charge me" screen once, then only
// re-checks the battery every LOW_BATT_RECHECK_S instead of browning out on
// every wake. It resumes once the battery reaches LOW_BATT_RECOVER_MV (the
// gap stops it flapping around the cutoff). Readings under NO_BATT_SENSE_MV
// mean the battery-sense divider is absent (some clones), so the cutoff is
// skipped rather than bricking the board.
constexpr uint32_t LOW_BATT_MV         = 3400;
constexpr uint32_t LOW_BATT_RECOVER_MV = 3600;
constexpr uint32_t NO_BATT_SENSE_MV    = 2500;
constexpr uint64_t LOW_BATT_RECHECK_S  = 2ULL * 60ULL * 60ULL;

// Sleep used when the server's X-Sleep-Seconds header is missing or out of
// range (MIN_SLEEP_S..MAX_SLEEP_S).
constexpr uint64_t DEFAULT_SLEEP_S = 30ULL * 60ULL;
constexpr long     MIN_SLEEP_S     = 60;
constexpr long     MAX_SLEEP_S     = 2L * 60L * 60L;

// Failed wakes back off 5 -> 15 -> 30 -> 60 min. The error screen replaces the
// calendar only after ERROR_SCREEN_AFTER consecutive failures, and is drawn
// once, not on every retry.
constexpr uint8_t ERROR_SCREEN_AFTER = 3;

// Survive deep sleep (cleared on power-on or reset).
RTC_DATA_ATTR uint8_t rtcFailCount   = 0;
RTC_DATA_ATTR bool    rtcErrorShown  = false;
RTC_DATA_ATTR uint8_t rtcBssid[6]    = {0};
RTC_DATA_ATTR int32_t rtcChannel     = 0;   // 0 = no cached AP
RTC_DATA_ATTR bool    rtcLowBattShown = false;

void goToSleep(uint64_t seconds) {
    Serial.flush();
    esp_sleep_enable_timer_wakeup(seconds * 1000000ULL);
    esp_deep_sleep_start();
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
    WiFi.persistent(false);   // don't rewrite credentials to NVS flash every wake
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

// On success, *sleepSecs is the server's X-Sleep-Seconds, or DEFAULT_SLEEP_S
// when that header is absent or out of range.
bool fetchImage(uint8_t* buf, int batPct, int rssi, uint64_t* sleepSecs,
                char* reason, size_t reasonLen) {
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
    if (AUTH_TOKEN[0] != '\0') {
        http.addHeader("Authorization", String("Bearer ") + AUTH_TOKEN);
    }
    const char* headerKeys[] = {"X-Sleep-Seconds"};
    http.collectHeaders(headerKeys, 1);

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
    long serverSleep = http.header("X-Sleep-Seconds").toInt();
    *sleepSecs = (serverSleep >= MIN_SLEEP_S && serverSleep <= MAX_SLEEP_S)
                     ? (uint64_t)serverSleep
                     : DEFAULT_SLEEP_S;

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
        Serial.println(reason);
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

    uint32_t lowCutoff = rtcLowBattShown ? LOW_BATT_RECOVER_MV : LOW_BATT_MV;
    if (mv > NO_BATT_SENSE_MV && mv < lowCutoff) {
        Serial.println("battery low — rechecking later");
        if (!rtcLowBattShown) {
            char detail[48];
            snprintf(detail, sizeof(detail), "%u mV. Charge to resume.", mv);
            drawError("Battery low", detail, "");
            rtcLowBattShown = true;
        }
        goToSleep(LOW_BATT_RECHECK_S);
    }
    rtcLowBattShown = false;

    char status[40];
    snprintf(status, sizeof(status), "battery %d%%", batPct);
    if (!connectWiFi()) {
        failAndSleep("WiFi connect failed", wifiStatusStr(WiFi.status()), status);
    }
    int rssi = WiFi.RSSI();
    Serial.printf("rssi: %d dBm\n", rssi);
    snprintf(status, sizeof(status), "battery %d%%  rssi %d dBm", batPct, rssi);

    uint8_t* buf = (uint8_t*)malloc(BUF_BYTES);
    if (!buf) {
        failAndSleep("Out of memory", "malloc failed", status);
    }

    char reason[64];
    uint64_t sleepSecs = DEFAULT_SLEEP_S;
    if (!fetchImage(buf, batPct, rssi, &sleepSecs, reason, sizeof(reason))) {
        free(buf);
        failAndSleep("Calendar fetch failed", reason, status);
    }
    drawBuffer(buf);
    free(buf);

    rtcFailCount = 0;
    rtcErrorShown = false;
    WiFi.disconnect(true);
    WiFi.mode(WIFI_OFF);
    Serial.printf("sleeping %llus\n", sleepSecs);
    goToSleep(sleepSecs);
}

void loop() {}
