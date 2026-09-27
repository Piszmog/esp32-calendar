package calendar_test

import (
	"bytes"
	"flag"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"calendar-display/internal/calendar"

	"github.com/stretchr/testify/require"
)

var update = flag.Bool("update", false, "rewrite golden files in testdata/")

const goldenPath = "testdata/golden.png"

// TestRenderImage_Golden renders a fixed, busy calendar and compares the
// packed bitmap the device would draw against testdata/golden.png. Fonts are
// embedded and rasterized in pure Go, so the output is the same everywhere.
// After an intended layout change, regenerate with:
//
//	go test ./internal/calendar -run Golden -update
//
// On a mismatch the actual image is written to the test's artifact dir;
// pass -artifacts to keep it.
func TestRenderImage_Golden(t *testing.T) {
	t.Parallel()
	loc, err := time.LoadLocation("America/Los_Angeles")
	require.NoError(t, err)
	day := func(d, h, m int) time.Time { return time.Date(2026, 5, d, h, m, 0, 0, loc) }
	now := day(11, 9, 0) // Monday

	events := make([]calendar.Event, 0, 14)
	events = append(events,
		calendar.Event{Title: "Team sync", Start: day(11, 9, 30), End: day(11, 9, 45)},
		calendar.Event{Title: "Design review with a title long enough to be truncated", Start: day(11, 13, 30), End: day(11, 15, 0)},
		calendar.Event{Title: "Holiday", Start: day(11, 0, 0), End: day(12, 0, 0), AllDay: true},
		calendar.Event{Title: "Offsite", Start: day(10, 18, 0), End: day(12, 12, 0)},
		calendar.Event{Title: "Dentist", Start: day(12, 8, 0), End: day(12, 9, 0)},
		calendar.Event{Title: "Conference", Start: day(13, 0, 0), End: day(16, 0, 0), AllDay: true},
	)
	for i := range 8 {
		events = append(events, calendar.Event{Title: "Busy block", Start: day(14, 8+i, 0), End: day(14, 8+i, 45)})
	}

	d := calendar.BuildDisplayData(events, loc, 42, -67, now)
	d.FetchedAt = now.Add(-5 * time.Minute)
	got := calendar.Pack1Bit(calendar.RenderImage(d))

	if *update {
		require.NoError(t, os.MkdirAll(filepath.Dir(goldenPath), 0o750))
		require.NoError(t, os.WriteFile(goldenPath, encodePacked(t, got), 0o644))
		return
	}

	f, err := os.Open(goldenPath)
	require.NoError(t, err, "missing golden file; run with -update")
	t.Cleanup(func() { _ = f.Close() })
	want, err := png.Decode(f)
	require.NoError(t, err)

	if !bytes.Equal(calendar.Pack1Bit(want), got) {
		out := filepath.Join(t.ArtifactDir(), "golden.actual.png")
		require.NoError(t, os.WriteFile(out, encodePacked(t, got), 0o644))
		t.Fatalf("render differs from %s; actual written to %s (rerun with -update if intended)", goldenPath, out)
	}
}

// encodePacked turns a pack1Bit buffer back into a two-color PNG, so the
// golden file shows exactly what the display draws.
func encodePacked(t *testing.T, packed []byte) []byte {
	t.Helper()
	img := image.NewPaletted(image.Rect(0, 0, calendar.ImgW, calendar.ImgH), color.Palette{color.Black, color.White})
	for i := range calendar.ImgW * calendar.ImgH {
		if packed[i/8]&(0x80>>(i%8)) != 0 {
			img.Pix[i] = 1
		}
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}
