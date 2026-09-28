package calendar_test

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"calendar-display/internal/calendar"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const firmwareDir = "../../../firmware/firebeetle_calendar/"

// TestFirmwareProtocolContract checks that the sketch still agrees with the
// server on the parts of the protocol that have no runtime handshake.
func TestFirmwareProtocolContract(t *testing.T) {
	t.Parallel()
	sketch, err := os.ReadFile(firmwareDir + "firebeetle_calendar.ino")
	require.NoError(t, err)
	logic, err := os.ReadFile(firmwareDir + "calendar_logic.h")
	require.NoError(t, err)
	ino := string(sketch) + string(logic)

	dim := func(name string) int {
		m := regexp.MustCompile(`constexpr\s+uint32_t\s+` + name + `\s*=\s*(\d+)\s*;`).FindStringSubmatch(ino)
		require.NotNil(t, m, "%s not found in sketch", name)
		n, err := strconv.Atoi(m[1])
		require.NoError(t, err)
		return n
	}
	assert.Equal(t, calendar.ImgW, dim("IMG_W"), "IMG_W must match imgW in render.go")
	assert.Equal(t, calendar.ImgH, dim("IMG_H"), "IMG_H must match imgH in render.go")

	// Pattern checks report by name only; dumping the whole sketch on failure is noise.
	for _, c := range []struct{ pattern, why string }{
		{`/calendar\.bin\?bat=%d&rssi=%d`, "sketch must request calendar.bin with the bat and rssi params statusFromQuery parses"},
		{`/calendar\.bin\?rssi=%d"`, "sketch must omit bat without a battery reading so statusFromQuery hides the icon"},
		{`drawInvertedBitmap\([^)]*GxEPD_BLACK\)`, "pack1Bit's bit=1=white convention pairs with drawInvertedBitmap(..., GxEPD_BLACK)"},
		{`"X-Sleep-Seconds"`, "sketch must read the sleep header handleBin sets"},
		{`"Authorization"`, "sketch must send the token requireToken checks"},
	} {
		assert.True(t, regexp.MustCompile(c.pattern).MatchString(ino), c.why)
	}
}

// TestSleepHeaderWithinFirmwareRange checks that every X-Sleep-Seconds value
// the server can send lies inside the range clampSleepSeconds accepts;
// anything outside is silently replaced by the firmware's 30-minute default.
func TestSleepHeaderWithinFirmwareRange(t *testing.T) {
	t.Parallel()
	logic, err := os.ReadFile(firmwareDir + "calendar_logic.h")
	require.NoError(t, err)

	// Evaluates constants written as a product of integer literals, e.g. 2L * 60L * 60L.
	constant := func(name string) int {
		m := regexp.MustCompile(`constexpr\s+\w+\s+` + name + `\s*=\s*([^;]+);`).FindSubmatch(logic)
		require.NotNil(t, m, "%s not found in calendar_logic.h", name)
		n := 1
		for factor := range strings.SplitSeq(string(m[1]), "*") {
			v, err := strconv.Atoi(strings.TrimRight(strings.TrimSpace(factor), "UL"))
			require.NoError(t, err, "%s: unsupported expression %q", name, m[1])
			n *= v
		}
		return n
	}
	minSleep, maxSleep := constant("MIN_SLEEP_S"), constant("MAX_SLEEP_S")

	// sleepSeconds depends only on minute and second, so one hour covers every input.
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for s := range 3600 {
		got := calendar.SleepSeconds(start.Add(time.Duration(s) * time.Second))
		if got < minSleep || got > maxSleep {
			t.Fatalf("sleepSeconds at +%ds = %d, outside firmware range [%d, %d]", s, got, minSleep, maxSleep)
		}
	}
}
