// Pure wake-cycle logic, kept free of Arduino headers so it can be unit-tested
// on the host (firmware/test/logic_test.cpp).
#pragma once

#include <stddef.h>
#include <stdint.h>
#include <stdio.h>

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

// Returns seconds to sleep after the given number of consecutive failures.
inline uint64_t backoffSeconds(uint8_t failures) {
    switch (failures) {
        case 0:
        case 1:  return 5ULL * 60ULL;
        case 2:  return 15ULL * 60ULL;
        case 3:  return 30ULL * 60ULL;
        default: return 60ULL * 60ULL;
    }
}

struct FailStep {
    uint8_t  failCount;   // new consecutive-failure count
    bool     drawError;   // draw the error screen on this wake
    uint64_t sleepSecs;
};

// Advances the failure streak for one failed wake. errorShown is whether the
// error screen is already up from an earlier wake in this streak.
inline FailStep nextFailure(uint8_t failCount, bool errorShown) {
    if (failCount < UINT8_MAX) failCount++;
    return {failCount,
            failCount >= ERROR_SCREEN_AFTER && !errorShown,
            backoffSeconds(failCount)};
}

// Map battery voltage (mV) to percentage (0-100) using a simple LiPo curve.
inline int batteryPercent(uint32_t mv) {
    if (mv >= 4100) return 100;
    if (mv >= 3950) return 75 + (mv - 3950) * 25 / 150;
    if (mv >= 3800) return 50 + (mv - 3800) * 25 / 150;
    if (mv >= 3700) return 25 + (mv - 3700) * 25 / 100;
    if (mv >= 3500) return     (mv - 3500) * 25 / 200;
    return 0;
}

// False when the reading means there is no battery-sense divider.
inline bool batterySensed(uint32_t mv) { return mv > NO_BATT_SENSE_MV; }

// Whether to stop and wait for a charge. lowShown is whether the low-battery
// screen is already up, which raises the cutoff to LOW_BATT_RECOVER_MV.
inline bool batteryTooLow(uint32_t mv, bool lowShown) {
    uint32_t cutoff = lowShown ? LOW_BATT_RECOVER_MV : LOW_BATT_MV;
    return batterySensed(mv) && mv < cutoff;
}

struct BatteryStep {
    bool stop;       // skip the fetch and sleep LOW_BATT_RECHECK_S
    bool drawLow;    // draw the low-battery screen on this wake
    bool lowShown;   // new lowShown, to store once any drawing is done
};

// Battery decision for one wake: draws the low-battery screen once per low
// spell and clears it once the battery recovers.
inline BatteryStep batteryStep(uint32_t mv, bool lowShown) {
    if (batteryTooLow(mv, lowShown)) {
        return {true, !lowShown, true};
    }
    return {false, false, false};
}

// Checks a response with a positive HTTP status (negative codes are
// transport errors, reported by the caller). On failure writes the reason
// shown on the error screen.
inline bool checkResponse(int code, int len, uint32_t expectedLen,
                          char* reason, size_t reasonLen) {
    if (code != 200) {
        snprintf(reason, reasonLen, "HTTP %d", code);
        return false;
    }
    if (len != (int)expectedLen) {
        snprintf(reason, reasonLen, "size %d, expected %u", len, (unsigned)expectedLen);
        return false;
    }
    return true;
}

// Seconds to sleep for a X-Sleep-Seconds header value (0 when absent).
inline uint64_t clampSleepSeconds(long serverSleep) {
    return (serverSleep >= MIN_SLEEP_S && serverSleep <= MAX_SLEEP_S)
               ? (uint64_t)serverSleep
               : DEFAULT_SLEEP_S;
}

// millis() at which to wake, sleepSecs after nowMs. Wraps with millis().
inline uint32_t wakeAtMillis(uint32_t nowMs, uint64_t sleepSecs) {
    return nowMs + (uint32_t)(sleepSecs * 1000ULL);
}

// Whole seconds from nowMs until wakeAtMs, at least 1 (also when already past).
inline uint64_t remainingSleepSeconds(uint32_t wakeAtMs, uint32_t nowMs) {
    int32_t remainingMs = (int32_t)(wakeAtMs - nowMs);
    return remainingMs > 1000 ? (uint64_t)remainingMs / 1000ULL : 1ULL;
}

// Writes the request path and query. batPct < 0 means no battery reading, so
// bat is omitted and the server hides the icon.
inline void formatCalendarPath(char* out, size_t outLen, int batPct, int rssi) {
    if (batPct >= 0) {
        snprintf(out, outLen, "/calendar.bin?bat=%d&rssi=%d", batPct, rssi);
    } else {
        snprintf(out, outLen, "/calendar.bin?rssi=%d", rssi);
    }
}
