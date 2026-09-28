// Host-side tests for calendar_logic.h. Build and run from the repo root:
//   c++ -std=c++17 -Wall -Wextra -Werror -I firmware/firebeetle_calendar
//       firmware/test/logic_test.cpp -o logic_test && ./logic_test

#include "calendar_logic.h"

#include <climits>
#include <cstring>

static int failures = 0;

#define CHECK_EQ(got, want)                                                  \
    do {                                                                     \
        long long g_ = (long long)(got);                                     \
        long long w_ = (long long)(want);                                    \
        if (g_ != w_) {                                                      \
            printf("%s:%d: %s = %lld, want %lld\n", __FILE__, __LINE__, #got, \
                   g_, w_);                                                  \
            failures++;                                                      \
        }                                                                    \
    } while (0)

#define CHECK_STR(got, want)                                                  \
    do {                                                                      \
        if (strcmp((got), (want)) != 0) {                                     \
            printf("%s:%d: got \"%s\", want \"%s\"\n", __FILE__, __LINE__,    \
                   (got), (want));                                            \
            failures++;                                                       \
        }                                                                     \
    } while (0)

static void testBatteryPercent() {
    CHECK_EQ(batteryPercent(5000), 100);
    CHECK_EQ(batteryPercent(4100), 100);
    CHECK_EQ(batteryPercent(4099), 99);
    CHECK_EQ(batteryPercent(3950), 75);
    CHECK_EQ(batteryPercent(3800), 50);
    CHECK_EQ(batteryPercent(3700), 25);
    CHECK_EQ(batteryPercent(3600), 12);
    CHECK_EQ(batteryPercent(3500), 0);
    CHECK_EQ(batteryPercent(3499), 0);
    CHECK_EQ(batteryPercent(0), 0);

    int prev = 0;
    for (uint32_t mv = 3000; mv <= 4300; mv++) {
        int p = batteryPercent(mv);
        if (p < prev || p > 100) {
            printf("batteryPercent(%u) = %d after %d: not monotonic in 0..100\n", mv, p, prev);
            failures++;
            break;
        }
        prev = p;
    }
}

static void testBackoffSeconds() {
    CHECK_EQ(backoffSeconds(0), 5 * 60);
    CHECK_EQ(backoffSeconds(1), 5 * 60);
    CHECK_EQ(backoffSeconds(2), 15 * 60);
    CHECK_EQ(backoffSeconds(3), 30 * 60);
    CHECK_EQ(backoffSeconds(4), 60 * 60);
    CHECK_EQ(backoffSeconds(UINT8_MAX), 60 * 60);
}

static void testNextFailure() {
    FailStep s = nextFailure(0, false);
    CHECK_EQ(s.failCount, 1);
    CHECK_EQ(s.drawError, false);
    CHECK_EQ(s.sleepSecs, 5 * 60);

    s = nextFailure(ERROR_SCREEN_AFTER - 1, false);
    CHECK_EQ(s.failCount, ERROR_SCREEN_AFTER);
    CHECK_EQ(s.drawError, true);

    s = nextFailure(ERROR_SCREEN_AFTER - 1, true);
    CHECK_EQ(s.drawError, false);

    // Saturates instead of wrapping back to 0, which would restart the backoff.
    s = nextFailure(UINT8_MAX, true);
    CHECK_EQ(s.failCount, UINT8_MAX);
    CHECK_EQ(s.sleepSecs, 60 * 60);
}

// Over a long streak the error screen is drawn exactly once, on the
// ERROR_SCREEN_AFTER-th failure.
static void testFailStreakDrawsOnce() {
    uint8_t count = 0;
    bool shown = false;
    int draws = 0;
    for (int i = 1; i <= 300; i++) {
        FailStep s = nextFailure(count, shown);
        count = s.failCount;
        if (s.drawError) {
            draws++;
            shown = true;
            CHECK_EQ(i, ERROR_SCREEN_AFTER);
        }
    }
    CHECK_EQ(draws, 1);
}

static void testBatteryThresholds() {
    CHECK_EQ(batterySensed(0), false);
    CHECK_EQ(batterySensed(NO_BATT_SENSE_MV), false);
    CHECK_EQ(batterySensed(NO_BATT_SENSE_MV + 1), true);

    CHECK_EQ(batteryTooLow(LOW_BATT_MV - 1, false), true);
    CHECK_EQ(batteryTooLow(LOW_BATT_MV, false), false);
    // Hysteresis: once the low screen is up, it takes LOW_BATT_RECOVER_MV to resume.
    CHECK_EQ(batteryTooLow(3500, false), false);
    CHECK_EQ(batteryTooLow(3500, true), true);
    CHECK_EQ(batteryTooLow(LOW_BATT_RECOVER_MV - 1, true), true);
    CHECK_EQ(batteryTooLow(LOW_BATT_RECOVER_MV, true), false);
    // No sense divider: never treated as low.
    CHECK_EQ(batteryTooLow(0, false), false);
    CHECK_EQ(batteryTooLow(NO_BATT_SENSE_MV, true), false);
}

// A low spell draws the screen once, holds through the hysteresis band, and
// clears once the battery reaches LOW_BATT_RECOVER_MV.
static void testBatteryStepSequence() {
    struct Wake { uint32_t mv; bool stop; bool drawLow; };
    const Wake wakes[] = {
        {3700, false, false},
        {3390, true,  true},    // drops below LOW_BATT_MV: draw once
        {3380, true,  false},
        {3500, true,  false},   // above LOW_BATT_MV but under RECOVER: still low
        {3599, true,  false},
        {3600, false, false},   // recovered
        {3500, false, false},   // band again, but not low from a normal state
        {3390, true,  true},    // new low spell draws again
    };
    bool lowShown = false;
    for (const Wake& w : wakes) {
        BatteryStep s = batteryStep(w.mv, lowShown);
        CHECK_EQ(s.stop, w.stop);
        CHECK_EQ(s.drawLow, w.drawLow);
        lowShown = s.lowShown;
    }
    // No sense divider never stops, even with a stale low flag.
    BatteryStep none = batteryStep(0, true);
    CHECK_EQ(none.stop, false);
    CHECK_EQ(none.lowShown, false);
}

static void testCheckResponse() {
    char reason[64];   // matches setup()'s buffer
    strcpy(reason, "untouched");
    CHECK_EQ(checkResponse(200, 48000, 48000, reason, sizeof(reason)), true);
    CHECK_STR(reason, "untouched");

    CHECK_EQ(checkResponse(401, 12, 48000, reason, sizeof(reason)), false);
    CHECK_STR(reason, "HTTP 401");   // AUTH_TOKEN mismatch
    CHECK_EQ(checkResponse(404, 48000, 48000, reason, sizeof(reason)), false);
    CHECK_STR(reason, "HTTP 404");
    CHECK_EQ(checkResponse(500, 48000, 48000, reason, sizeof(reason)), false);
    CHECK_STR(reason, "HTTP 500");

    CHECK_EQ(checkResponse(200, 47999, 48000, reason, sizeof(reason)), false);
    CHECK_STR(reason, "size 47999, expected 48000");
    CHECK_EQ(checkResponse(200, -1, 48000, reason, sizeof(reason)), false);
    CHECK_STR(reason, "size -1, expected 48000");   // chunked: no Content-Length
    CHECK_EQ(checkResponse(200, INT_MIN, 48000, reason, sizeof(reason)), false);
    CHECK_STR(reason, "size -2147483648, expected 48000");   // widest reason fits
}

static void testClampSleepSeconds() {
    CHECK_EQ(clampSleepSeconds(0), DEFAULT_SLEEP_S);   // header absent
    CHECK_EQ(clampSleepSeconds(-5), DEFAULT_SLEEP_S);
    CHECK_EQ(clampSleepSeconds(MIN_SLEEP_S - 1), DEFAULT_SLEEP_S);
    CHECK_EQ(clampSleepSeconds(MIN_SLEEP_S), MIN_SLEEP_S);
    CHECK_EQ(clampSleepSeconds(1234), 1234);
    CHECK_EQ(clampSleepSeconds(MAX_SLEEP_S), MAX_SLEEP_S);
    CHECK_EQ(clampSleepSeconds(MAX_SLEEP_S + 1), DEFAULT_SLEEP_S);
    CHECK_EQ(clampSleepSeconds(LONG_MAX), DEFAULT_SLEEP_S);
}

static void testSleepAcrossMillisWrap() {
    const uint32_t now = UINT32_MAX - 15;
    uint32_t wake = wakeAtMillis(now, 60);
    CHECK_EQ(wake, 59984u);
    CHECK_EQ(remainingSleepSeconds(wake, now), 60);
    CHECK_EQ(remainingSleepSeconds(wake, now + 500), 59);   // draw time comes out of the sleep
    CHECK_EQ(remainingSleepSeconds(wake, wake - 1000), 1);
    CHECK_EQ(remainingSleepSeconds(wake, wake - 500), 1);   // not a zero-second sleep
    CHECK_EQ(remainingSleepSeconds(wake, wake + 5000), 1);  // already past: minimum sleep

    CHECK_EQ(remainingSleepSeconds(wakeAtMillis(1000, MAX_SLEEP_S), 1000), MAX_SLEEP_S);
}

static void testFormatCalendarPath() {
    char buf[48];
    formatCalendarPath(buf, sizeof(buf), 87, -60);
    CHECK_STR(buf, "/calendar.bin?bat=87&rssi=-60");
    formatCalendarPath(buf, sizeof(buf), -1, -60);
    CHECK_STR(buf, "/calendar.bin?rssi=-60");
    formatCalendarPath(buf, sizeof(buf), 0, 0);
    CHECK_STR(buf, "/calendar.bin?bat=0&rssi=0");
    // The firmware's 48-byte buffer fits the widest values without truncating.
    formatCalendarPath(buf, sizeof(buf), 100, INT_MIN);
    CHECK_STR(buf, "/calendar.bin?bat=100&rssi=-2147483648");
}

int main() {
    testBatteryPercent();
    testBackoffSeconds();
    testNextFailure();
    testFailStreakDrawsOnce();
    testBatteryThresholds();
    testBatteryStepSequence();
    testCheckResponse();
    testClampSleepSeconds();
    testSleepAcrossMillisWrap();
    testFormatCalendarPath();
    if (failures) {
        printf("%d failure(s)\n", failures);
        return 1;
    }
    printf("ok\n");
    return 0;
}
