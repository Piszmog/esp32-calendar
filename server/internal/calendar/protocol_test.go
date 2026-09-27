package calendar_test

import (
	"os"
	"regexp"
	"strconv"
	"testing"

	"calendar-display/internal/calendar"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const firmwarePath = "../../../firmware/firebeetle_calendar/firebeetle_calendar.ino"

// TestFirmwareProtocolContract checks that the sketch still agrees with the
// server on the parts of the protocol that have no runtime handshake.
func TestFirmwareProtocolContract(t *testing.T) {
	t.Parallel()
	src, err := os.ReadFile(firmwarePath)
	require.NoError(t, err)
	ino := string(src)

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
