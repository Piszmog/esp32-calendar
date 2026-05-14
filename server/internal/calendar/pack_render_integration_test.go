package calendar_test

import (
	"testing"
	"time"

	"calendar-display/internal/calendar"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPackRender_ProtocolContract verifies the 800×480 → 48000-byte invariant
// documented in CLAUDE.md. This is the end-to-end path the ESP32 firmware
// depends on; a size mismatch would cause the device to silently skip the
// refresh without any version handshake.
func TestPackRender_ProtocolContract(t *testing.T) {
	t.Parallel()
	loc := time.UTC
	now := time.Date(2026, 5, 11, 12, 0, 0, 0, loc)

	events := []calendar.Event{
		{Start: now.Add(1 * time.Hour), Title: "Test Event"},
		{Start: time.Date(2026, 5, 12, 9, 0, 0, 0, loc), Title: testTitleTomorrow},
	}

	d := calendar.BuildDisplayData(events, loc, 72, -65, now)
	img, err := calendar.RenderImage(d)
	require.NoError(t, err)

	packed := calendar.Pack1Bit(img)
	assert.Len(t, packed, 48000, "800×480 pixels must pack to exactly 48000 bytes")
}

func TestPackRender_WhiteBackground(t *testing.T) {
	t.Parallel()
	loc := time.UTC
	now := time.Date(2026, 5, 11, 12, 0, 0, 0, loc)
	d := calendar.BuildDisplayData(nil, loc, -1, 0, now)
	img, err := calendar.RenderImage(d)
	require.NoError(t, err)

	packed := calendar.Pack1Bit(img)

	// The header background is white. Bytes covering the top-left corner of
	// the image (x=0..7, y=0) should all be 0xFF.
	for i := range 5 {
		assert.Equal(t, byte(0xFF), packed[i], "header byte %d should be 0xFF (white background)", i)
	}
}
