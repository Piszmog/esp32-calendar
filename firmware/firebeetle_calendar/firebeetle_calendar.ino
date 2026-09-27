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
 *                  and set EPD_PWR below to it.)
 */

#include <WiFi.h>
#include <HTTPClient.h>
#include <GxEPD2_BW.h>
#include "esp_sleep.h"
#include "esp_task_wdt.h"
#include "driver/gpio.h"
#include "calendar_logic.h"

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
// GPIO wired to the HAT's PWR pin, or -1 when PWR is tied to 3V3. When set,
// the display is powered only while drawing and PWR is held LOW in deep sleep.
constexpr int8_t EPD_PWR = -1;

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

// Static rather than malloc'd each wake, so there's no out-of-memory path.
static uint8_t imgBuf[BUF_BYTES];

// Whole wake cycle must finish within this, or the task watchdog resets the
// board (worst case: 28 s WiFi + 20 s fetch + ~20 s display).
constexpr uint32_t WDT_TIMEOUT_MS = 120000;

// Single deadline for the HTTP fetch, from request start to last byte.
constexpr uint32_t FETCH_DEADLINE_MS = 20000;

// Survive deep sleep (cleared on power-on or reset).
RTC_DATA_ATTR uint8_t rtcFailCount   = 0;
RTC_DATA_ATTR bool    rtcErrorShown  = false;
RTC_DATA_ATTR uint8_t rtcBssid[6]    = {0};
RTC_DATA_ATTR int32_t rtcChannel     = 0;   // 0 = no cached AP
RTC_DATA_ATTR bool    rtcLowBattShown = false;

// Switches display power via EPD_PWR. Off also latches the pin LOW through
// deep sleep; on releases that latch first. No-op when EPD_PWR is -1.
void displayPower(bool on) {
    if (EPD_PWR < 0) return;
    gpio_num_t pin = (gpio_num_t)EPD_PWR;
    gpio_hold_dis(pin);
    pinMode(EPD_PWR, OUTPUT);
    digitalWrite(EPD_PWR, on ? HIGH : LOW);
    if (on) {
        delay(10);   // let the HAT's supply settle before init
    } else {
        gpio_hold_en(pin);
        gpio_deep_sleep_hold_en();
    }
}

void wifiOff() {
    WiFi.disconnect(true);
    WiFi.mode(WIFI_OFF);
}

void goToSleep(uint64_t seconds) {
    Serial.flush();
    esp_sleep_enable_timer_wakeup(seconds * 1000000ULL);
    esp_deep_sleep_start();
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
#ifdef STATIC_IP
    // Skips DHCP, which otherwise keeps the radio on for another second or two.
    WiFi.config(IPAddress(STATIC_IP), IPAddress(STATIC_GATEWAY),
                IPAddress(STATIC_SUBNET), IPAddress(STATIC_DNS));
#endif
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

// On success, *wakeAtMs is the millis() at which to wake: the server's
// X-Sleep-Seconds (or DEFAULT_SLEEP_S when that header is absent or out of
// range) counted from when the response arrived, so the time spent drawing
// comes out of the sleep. batPct < 0 means no battery reading; bat is omitted.
bool fetchImage(uint8_t* buf, int batPct, int rssi, uint32_t* wakeAtMs,
                char* reason, size_t reasonLen) {
    char path[48];
    formatCalendarPath(path, sizeof(path), batPct, rssi);
    char url[160];
    snprintf(url, sizeof(url), "http://%s:%u%s", SERVER_HOST, SERVER_PORT, path);

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
    if (code <= 0) {
        snprintf(reason, reasonLen, "%s", http.errorToString(code).c_str());
        Serial.println(reason);
        http.end();
        return false;
    }
    if (!checkResponse(code, http.getSize(), BUF_BYTES, reason, reasonLen)) {
        Serial.println(reason);
        http.end();
        return false;
    }
    uint64_t sleepSecs = clampSleepSeconds(http.header("X-Sleep-Seconds").toInt());
    *wakeAtMs = wakeAtMillis(millis(), sleepSecs);

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
    displayPower(true);
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
    displayPower(false);
}

void drawError(const char* title, const char* detail, const char* statusLine) {
    displayPower(true);
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
    displayPower(false);
}

// Records a failed wake, shows the error screen once the failure streak
// reaches ERROR_SCREEN_AFTER (leaving the last calendar up until then), and
// sleeps with backoff. Does not return.
void failAndSleep(const char* title, const char* detail, const char* statusLine) {
    FailStep step = nextFailure(rtcFailCount, rtcErrorShown);
    rtcFailCount = step.failCount;
    Serial.printf("failure %u: %s: %s\n", rtcFailCount, title, detail);
    wifiOff();   // before drawing: the radio isn't needed during the refresh
    if (step.drawError) {
        drawError(title, detail, statusLine);
        rtcErrorShown = true;
    }
    Serial.printf("sleeping %llus\n", step.sleepSecs);
    goToSleep(step.sleepSecs);
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
    bool battSensed = batterySensed(mv);
    int batPct = battSensed ? batteryPercent(mv) : -1;
    Serial.printf("battery: %u mV (%d%%)\n", mv, batPct);

    BatteryStep batt = batteryStep(mv, rtcLowBattShown);
    if (batt.stop) {
        Serial.println("battery low — rechecking later");
        if (batt.drawLow) {
            char detail[48];
            snprintf(detail, sizeof(detail), "%u mV. Charge to resume.", mv);
            drawError("Battery low", detail, "");
        }
        rtcLowBattShown = batt.lowShown;
        goToSleep(LOW_BATT_RECHECK_S);
    }
    rtcLowBattShown = batt.lowShown;

    char batStr[8];
    if (battSensed) {
        snprintf(batStr, sizeof(batStr), "%d%%", batPct);
    } else {
        snprintf(batStr, sizeof(batStr), "n/a");
    }
    char status[40];
    snprintf(status, sizeof(status), "battery %s", batStr);
    if (!connectWiFi()) {
        failAndSleep("WiFi connect failed", wifiStatusStr(WiFi.status()), status);
    }
    int rssi = WiFi.RSSI();
    Serial.printf("rssi: %d dBm\n", rssi);
    snprintf(status, sizeof(status), "battery %s  rssi %d dBm", batStr, rssi);

    char reason[64];
    uint32_t wakeAtMs = 0;
    if (!fetchImage(imgBuf, batPct, rssi, &wakeAtMs, reason, sizeof(reason))) {
        failAndSleep("Calendar fetch failed", reason, status);
    }
    wifiOff();   // before drawing: the radio isn't needed during the refresh
    drawBuffer(imgBuf);

    rtcFailCount = 0;
    rtcErrorShown = false;
    uint64_t sleepSecs = remainingSleepSeconds(wakeAtMs, millis());
    Serial.printf("sleeping %llus\n", sleepSecs);
    goToSleep(sleepSecs);
}

void loop() {}
