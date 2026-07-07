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
#include <ESPmDNS.h>
#include <GxEPD2_BW.h>
#include "esp_sleep.h"
#include "esp_adc_cal.h"
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

bool connectWiFi() {
    WiFi.mode(WIFI_STA);
    WiFi.begin(WIFI_SSID, WIFI_PASS);
    uint32_t t0 = millis();
    while (WiFi.status() != WL_CONNECTED && millis() - t0 < 20000) {
        delay(250);
        Serial.print('.');
    }
    Serial.println();
    return WiFi.status() == WL_CONNECTED;
}

// Read battery voltage on FireBeetle 2 ESP32-E (GPIO34, 1:2 divider).
// Returns voltage in millivolts.
uint32_t readBatteryMv() {
    esp_adc_cal_characteristics_t adc_chars;
    esp_adc_cal_characterize(ADC_UNIT_1, ADC_ATTEN_DB_11,
                             ADC_WIDTH_BIT_12, 1100, &adc_chars);

    // Discard first two reads: ESP32 SAR ADC produces a noisier sample
    // on the first call after deep-sleep wake or cold boot.
    analogRead(BATT_ADC_PIN);
    analogRead(BATT_ADC_PIN);

    // Average a few reads to smooth noise.
    uint32_t raw = 0;
    const int N = 16;
    for (int i = 0; i < N; i++) {
        raw += analogRead(BATT_ADC_PIN);
        delay(2);
    }
    raw /= N;

    uint32_t pin_mv = esp_adc_cal_raw_to_voltage(raw, &adc_chars);
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

bool fetchImage(uint8_t* buf, int batPct, int rssi) {
    char url[160];
    snprintf(url, sizeof(url),
             "http://%s:%u/calendar.bin?bat=%d&rssi=%d",
             SERVER_HOST, SERVER_PORT, batPct, rssi);

    Serial.printf("GET %s\n", url);

    HTTPClient http;
    http.setTimeout(20000);
    if (!http.begin(url)) return false;

    int code = http.GET();
    if (code != HTTP_CODE_OK) {
        Serial.printf("HTTP %d\n", code);
        http.end();
        return false;
    }
    int len = http.getSize();
    if (len != (int)BUF_BYTES) {
        Serial.printf("size %d, expected %u\n", len, BUF_BYTES);
        http.end();
        return false;
    }

    WiFiClient* s = http.getStreamPtr();
    if (!s) { http.end(); return false; }
    uint32_t got = 0, t0 = millis();
    while (got < BUF_BYTES && millis() - t0 < 20000) {
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

void setup() {
    Serial.begin(115200);
    delay(100);
    Serial.println("\n== calendar wake ==");

    // Read battery BEFORE WiFi powers up (cleaner reading).
    uint32_t mv = readBatteryMv();
    int batPct = batteryPercent(mv);
    Serial.printf("battery: %u mV (%d%%)\n", mv, batPct);

    if (!connectWiFi()) {
        Serial.println("wifi failed");
        goToSleep(5ULL * 60ULL);
    }
    int rssi = WiFi.RSSI();
    Serial.printf("rssi: %d dBm\n", rssi);

    size_t hostLen = strlen(SERVER_HOST);
    if (hostLen > 6 && strcmp(SERVER_HOST + hostLen - 6, ".local") == 0) {
        if (!MDNS.begin("firebeetle-calendar")) {
            Serial.println("mDNS init failed");
        }
    }

    configTime(0, 0, "pool.ntp.org", "time.google.com");
    for (int i = 0; i < 20 && time(nullptr) < 1704067200; i++) delay(100);

    uint8_t* buf = (uint8_t*)malloc(BUF_BYTES);
    if (!buf) {
        Serial.println("malloc failed");
        goToSleep(5ULL * 60ULL);
    }

    bool fetchOk = fetchImage(buf, batPct, rssi);
    if (fetchOk) {
        drawBuffer(buf);
    } else {
        Serial.println("fetch failed — skipping refresh");
    }

    free(buf);
    WiFi.disconnect(true);
    WiFi.mode(WIFI_OFF);
    uint64_t sleepSecs = fetchOk ? nextWakeSeconds() : 5ULL * 60ULL;
    Serial.printf("sleeping %llus\n", sleepSecs);
    goToSleep(sleepSecs);
}

void loop() {}
