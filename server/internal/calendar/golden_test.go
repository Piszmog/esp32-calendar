package calendar_test

import (
	"bytes"
	"flag"
	"image"
	"image/color"
	"image/png"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"calendar-display/internal/calendar"

	"github.com/stretchr/testify/require"
)

var update = flag.Bool("update", false, "rewrite golden files in testdata/")

const goldenDir = "testdata/golden"

// goldenLoc is the zone every golden scenario renders in.
func goldenLoc(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/Los_Angeles")
	require.NoError(t, err)
	return loc
}

// goldenDay returns May d, 2026 at h:m in loc; May 11 2026 is a Monday.
func goldenDay(loc *time.Location, d, h, m int) time.Time {
	return time.Date(2026, 5, d, h, m, 0, 0, loc)
}

// TestRenderImage_Golden renders fixed calendar scenarios and compares the
// packed bitmap the device would draw against testdata/golden/<name>.png.
// Fonts are embedded and rasterized in pure Go, so the output is the same
// everywhere. After an intended layout change, regenerate with:
//
//	go test ./internal/calendar -run Golden -update
//
// and look at every changed PNG before committing. On a mismatch the actual
// image and a diff (differing pixels in black) are written to the test's
// artifact dir; pass -artifacts to keep them.
func TestRenderImage_Golden(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		data func(t *testing.T, loc *time.Location) calendar.DisplayData
	}{
		{"busy", goldenBusy},
		{"empty", goldenEmpty},
		{"stale", goldenStale},
		{"today-overflow", goldenTodayOverflow},
		{"low-battery", goldenLowBattery},
		{"edge-times", goldenEdgeTimes},
		{"glyphs", goldenGlyphs},
		{"titles", goldenTitles},
		{"both-overflow", goldenBothOverflow},
		{"tomorrow-overflow", goldenTomorrowOverflow},
		{"week-edges", goldenWeekEdges},
		{"all-day-stack", goldenAllDayStack},
		{"dst-fall-back", goldenDSTFallBack},
		{"month-boundary", goldenMonthBoundary},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := tc.data(t, goldenLoc(t))
			checkGolden(t, tc.name, calendar.Pack1Bit(calendar.RenderImage(d)))
		})
	}
}

// TestDemoPNG_Golden fetches /calendar.demo.png over HTTP, so the route, the
// PNG encoding, and the README screenshot are all pinned. The demo uses a
// fixed date, so the image doesn't depend on the clock.
func TestDemoPNG_Golden(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(calendar.NewTestHandler(goldenLoc(t), nil, time.Now()))
	t.Cleanup(ts.Close)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+"/calendar.demo.png", nil)
	require.NoError(t, err)
	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	require.Equal(t, http.StatusOK, resp.StatusCode)

	img, err := png.Decode(resp.Body)
	require.NoError(t, err)
	checkGolden(t, "demo", calendar.Pack1Bit(img))
}

// goldenBusy is a busy Monday: a truncated chip, all-day and multi-day
// events, and a Week Ahead day with "+ N more".
func goldenBusy(_ *testing.T, loc *time.Location) calendar.DisplayData {
	day := func(d, h, m int) time.Time { return goldenDay(loc, d, h, m) }
	now := day(11, 9, 0)

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
	return d
}

// goldenEmpty has no events at all: both "nothing scheduled" lines, dashes
// for every Week Ahead day, no battery icon, and zero WiFi bars.
func goldenEmpty(_ *testing.T, loc *time.Location) calendar.DisplayData {
	return calendar.BuildDisplayData(nil, loc, -1, calendar.RSSIUnknown, goldenDay(loc, 11, 9, 0))
}

// goldenStale shows the "(stale)" footer with a two-hour-old fetch, a full
// battery, and four WiFi bars.
func goldenStale(_ *testing.T, loc *time.Location) calendar.DisplayData {
	day := func(d, h, m int) time.Time { return goldenDay(loc, d, h, m) }
	now := day(11, 14, 0)
	events := []calendar.Event{
		{Title: "Planning", Start: day(11, 15, 0), End: day(11, 16, 0)},
		{Title: "Morning sync", Start: day(12, 9, 0), End: day(12, 9, 15)},
		{Title: "Retro", Start: day(15, 11, 0), End: day(15, 12, 0)},
	}
	d := calendar.BuildDisplayData(events, loc, 100, -50, now)
	d.FetchedAt = now.Add(-2 * time.Hour)
	d.Stale = true
	return d
}

// goldenTodayOverflow has more Today events than the left column fits, so the
// last row becomes "+ N more", and nothing tomorrow.
func goldenTodayOverflow(_ *testing.T, loc *time.Location) calendar.DisplayData {
	now := goldenDay(loc, 11, 7, 0)
	events := make([]calendar.Event, 0, 12)
	for i := range 12 {
		start := goldenDay(loc, 11, 8+i, 0)
		events = append(events, calendar.Event{Title: "Meeting", Start: start, End: start.Add(30 * time.Minute)})
	}
	return calendar.BuildDisplayData(events, loc, 87, -60, now)
}

// goldenLowBattery is a near-empty battery with one WiFi bar and a sparse
// week: one event today, one tomorrow, one Week Ahead day.
func goldenLowBattery(_ *testing.T, loc *time.Location) calendar.DisplayData {
	day := func(d, h, m int) time.Time { return goldenDay(loc, d, h, m) }
	events := []calendar.Event{
		{Title: "Charge the display", Start: day(11, 18, 0), End: day(11, 18, 30)},
		{Title: "Groceries", Start: day(12, 17, 0), End: day(12, 18, 0)},
		{Title: "Birthday", Start: day(14, 0, 0), End: day(15, 0, 0), AllDay: true},
	}
	return calendar.BuildDisplayData(events, loc, 3, -80, day(11, 9, 0))
}

// goldenEdgeTimes covers clock edges: an event running since midnight, one
// that started inside the 30-minute cutoff, one hidden by it, noon, 22:00 and
// 23:59 starts (the last continuing past midnight), and an event ending
// exactly at midnight that must not show tomorrow.
func goldenEdgeTimes(_ *testing.T, loc *time.Location) calendar.DisplayData {
	day := func(d, h, m int) time.Time { return goldenDay(loc, d, h, m) }
	events := []calendar.Event{
		{Title: "Overnight job", Start: day(11, 0, 0), End: day(11, 9, 0)},
		{Title: "Hidden: ended", Start: day(11, 7, 0), End: day(11, 7, 15)},
		{Title: "Started 20 min ago", Start: day(11, 7, 40), End: day(11, 7, 55)},
		{Title: "Lunch", Start: day(11, 12, 0), End: day(11, 13, 0)},
		{Title: "Late show", Start: day(11, 22, 0), End: day(12, 0, 0)},
		{Title: "Deploy", Start: day(11, 23, 59), End: day(12, 0, 30)},
		{Title: "Red-eye", Start: day(12, 0, 0), End: day(12, 6, 0)},
	}
	return calendar.BuildDisplayData(events, loc, 55, -70, day(11, 8, 0))
}

// icsGlyphs has titles with glyphs the fonts lack; they go through the iCal
// parser, which is where dropMissingGlyphs runs.
const icsGlyphs = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Test//Test//EN
BEGIN:VEVENT
UID:emoji-only@test
SUMMARY:🎉🎂
DTSTART;TZID=America/Los_Angeles:20260511T100000
DTEND;TZID=America/Los_Angeles:20260511T110000
END:VEVENT
BEGIN:VEVENT
UID:mixed@test
SUMMARY:Pizza 🍕 night
DTSTART;TZID=America/Los_Angeles:20260511T180000
DTEND;TZID=America/Los_Angeles:20260511T200000
END:VEVENT
BEGIN:VEVENT
UID:accents@test
SUMMARY:Café résumé review – Zürich
DTSTART;TZID=America/Los_Angeles:20260512T090000
DTEND;TZID=America/Los_Angeles:20260512T100000
END:VEVENT
BEGIN:VEVENT
UID:week@test
SUMMARY:✈️ Flight to Łódź
DTSTART;TZID=America/Los_Angeles:20260514T070000
DTEND;TZID=America/Los_Angeles:20260514T120000
END:VEVENT
END:VCALENDAR`

// goldenGlyphs renders parsed titles: emoji-only becomes "(no title)", mixed
// titles keep their text, and accented Latin draws as-is.
func goldenGlyphs(t *testing.T, loc *time.Location) calendar.DisplayData {
	t.Helper()
	now := goldenDay(loc, 11, 9, 0)
	events, err := calendar.EventsFromICS(icsGlyphs, loc, now.AddDate(0, 0, -1), now.AddDate(0, 0, 7))
	require.NoError(t, err)
	return calendar.BuildDisplayData(events, loc, 70, -60, now)
}

// icsTitles has titles that are hard to sanitize: decomposed (NFD) accents,
// iCal escapes, an unbreakable word, CJK mixed with and without Latin, and
// blank or missing SUMMARY lines.
var icsTitles = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Test//Test//EN\r\n" +
	titleEvent("nfd", "Cafe\u0301 re\u0301sume\u0301", "20260511T100000") +
	titleEvent("escapes", `Budget\, Q3\; final\nreview`, "20260511T110000") +
	titleEvent("unbreakable", "Supercalifragilisticexpialidocious-extravaganza", "20260511T120000") +
	titleEvent("cjk-mixed", "会議 with Kenji", "20260511T130000") +
	titleEvent("cjk-only", "会議", "20260511T140000") +
	titleEvent("blank", "   ", "20260512T090000") +
	"BEGIN:VEVENT\r\nUID:nosummary@test\r\n" +
	"DTSTART;TZID=America/Los_Angeles:20260512T100000\r\n" +
	"DTEND;TZID=America/Los_Angeles:20260512T110000\r\nEND:VEVENT\r\n" +
	"END:VCALENDAR\r\n"

func titleEvent(uid, summary, start string) string {
	return "BEGIN:VEVENT\r\nUID:" + uid + "@test\r\nSUMMARY:" + summary + "\r\n" +
		"DTSTART;TZID=America/Los_Angeles:" + start + "\r\n" +
		"DURATION:PT30M\r\nEND:VEVENT\r\n"
}

// goldenTitles renders the parsed icsTitles events.
func goldenTitles(t *testing.T, loc *time.Location) calendar.DisplayData {
	t.Helper()
	now := goldenDay(loc, 11, 9, 0)
	events, err := calendar.EventsFromICS(icsTitles, loc, now.AddDate(0, 0, -1), now.AddDate(0, 0, 7))
	require.NoError(t, err)
	return calendar.BuildDisplayData(events, loc, 70, -60, now)
}

// hourly returns n 30-minute events starting at h:00 on day d, one per hour.
func hourly(loc *time.Location, title string, d, h, n int) []calendar.Event {
	events := make([]calendar.Event, 0, n)
	for i := range n {
		start := goldenDay(loc, d, h+i, 0)
		events = append(events, calendar.Event{Title: title, Start: start, End: start.Add(30 * time.Minute)})
	}
	return events
}

// goldenBothOverflow overflows Today and Tomorrow at once, so Tomorrow keeps
// its two reserved rows: one chip and "+ N more".
func goldenBothOverflow(_ *testing.T, loc *time.Location) calendar.DisplayData {
	events := append(hourly(loc, "Today", 11, 10, 8), hourly(loc, "Tomorrow", 12, 9, 6)...)
	return calendar.BuildDisplayData(events, loc, 50, -60, goldenDay(loc, 11, 9, 0))
}

// goldenTomorrowOverflow has nothing left today, so Today keeps one row and
// Tomorrow gets the rest of the column.
func goldenTomorrowOverflow(_ *testing.T, loc *time.Location) calendar.DisplayData {
	events := append(
		[]calendar.Event{{Title: "Already over", Start: goldenDay(loc, 11, 8, 0), End: goldenDay(loc, 11, 8, 15)}},
		hourly(loc, "Workshop", 12, 8, 12)...)
	return calendar.BuildDisplayData(events, loc, 50, -60, goldenDay(loc, 11, 17, 0))
}

// goldenWeekEdges stresses Week Ahead: a single over-long title, an
// over-long first event followed by more, a very busy day, mixed all-day and
// timed events, and a multi-day event across the rest of the week. The
// hourly interviews run past midnight into Saturday, where 00:00 shortens
// to "0".
func goldenWeekEdges(_ *testing.T, loc *time.Location) calendar.DisplayData {
	day := func(d, h, m int) time.Time { return goldenDay(loc, d, h, m) }
	events := make([]calendar.Event, 0, 24)
	events = append(events,
		calendar.Event{Title: "Quarterly business review with the entire leadership team", Start: day(13, 10, 0), End: day(13, 12, 0)},
		calendar.Event{Title: "All-hands", Start: day(14, 0, 0), End: day(15, 0, 0), AllDay: true},
		calendar.Event{Title: "Lunch", Start: day(14, 12, 30), End: day(14, 13, 0)},
		calendar.Event{Title: "Camping trip", Start: day(16, 0, 0), End: day(18, 0, 0), AllDay: true},
	)
	events = append(events, hourly(loc, "Interview", 15, 8, 20)...)
	return calendar.BuildDisplayData(events, loc, 60, -60, day(11, 9, 0))
}

// goldenAllDayStack has several all-day events today (one spanning the whole
// week) sorted ahead of timed ones.
func goldenAllDayStack(_ *testing.T, loc *time.Location) calendar.DisplayData {
	day := func(d, h, m int) time.Time { return goldenDay(loc, d, h, m) }
	events := []calendar.Event{
		{Title: "Huddle", Start: day(11, 9, 30), End: day(11, 9, 45)},
		{Title: "Vacation", Start: day(9, 0, 0), End: day(20, 0, 0), AllDay: true},
		{Title: "Mom's birthday", Start: day(11, 0, 0), End: day(12, 0, 0), AllDay: true},
		{Title: "Trash day", Start: day(11, 0, 0), End: day(12, 0, 0), AllDay: true},
	}
	return calendar.BuildDisplayData(events, loc, 60, -60, day(11, 9, 0))
}

// icsDST repeats a 09:00 meeting daily across the US fall-back on Sunday
// Nov 1 2026, and has two 01:30 events on that day: one PDT, one PST.
const icsDST = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Test//Test//EN
BEGIN:VEVENT
UID:daily@test
SUMMARY:Daily check-in
DTSTART;TZID=America/Los_Angeles:20261029T090000
DTEND;TZID=America/Los_Angeles:20261029T091500
RRULE:FREQ=DAILY;COUNT=10
END:VEVENT
BEGIN:VEVENT
UID:pdt@test
SUMMARY:First 01:30 (PDT)
DTSTART:20261101T083000Z
DTEND:20261101T084500Z
END:VEVENT
BEGIN:VEVENT
UID:pst@test
SUMMARY:Second 01:30 (PST)
DTSTART:20261101T093000Z
DTEND:20261101T094500Z
END:VEVENT
END:VCALENDAR`

// goldenDSTFallBack renders the day before fall-back: the recurrence stays at
// 09:00 on every day, and both 01:30s show tomorrow in order.
func goldenDSTFallBack(t *testing.T, loc *time.Location) calendar.DisplayData {
	t.Helper()
	now := time.Date(2026, 10, 31, 8, 0, 0, 0, loc)
	events, err := calendar.EventsFromICS(icsDST, loc, now.AddDate(0, 0, -1), now.AddDate(0, 0, 7))
	require.NoError(t, err)
	return calendar.BuildDisplayData(events, loc, 60, -60, now)
}

// goldenMonthBoundary is Wednesday, September 30, the widest header of 2026,
// with a Week Ahead that rolls into October.
func goldenMonthBoundary(_ *testing.T, loc *time.Location) calendar.DisplayData {
	now := time.Date(2026, 9, 30, 9, 0, 0, 0, loc)
	events := []calendar.Event{
		{Title: "Month-end close", Start: now.Add(time.Hour), End: now.Add(2 * time.Hour)},
		{Title: "Rent due", Start: time.Date(2026, 10, 1, 0, 0, 0, 0, loc), End: time.Date(2026, 10, 2, 0, 0, 0, 0, loc), AllDay: true},
		{Title: "Q4 kickoff", Start: time.Date(2026, 10, 5, 10, 0, 0, 0, loc), End: time.Date(2026, 10, 5, 11, 0, 0, 0, loc)},
	}
	return calendar.BuildDisplayData(events, loc, 60, -60, now)
}

// checkGolden compares packed against testdata/golden/<name>.png, or
// rewrites that file under -update.
func checkGolden(t *testing.T, name string, packed []byte) {
	t.Helper()
	path := filepath.Join(goldenDir, name+".png")
	if *update {
		require.NoError(t, os.MkdirAll(goldenDir, 0o750))
		require.NoError(t, os.WriteFile(path, encodePacked(t, packed), 0o644))
		return
	}

	raw, err := fs.ReadFile(os.DirFS(goldenDir), name+".png")
	require.NoError(t, err, "missing golden file; run with -update")
	want, err := png.Decode(bytes.NewReader(raw))
	require.NoError(t, err)
	wantPacked := calendar.Pack1Bit(want)

	if !bytes.Equal(wantPacked, packed) {
		actual := filepath.Join(t.ArtifactDir(), name+".actual.png")
		diff := filepath.Join(t.ArtifactDir(), name+".diff.png")
		require.NoError(t, os.WriteFile(actual, encodePacked(t, packed), 0o644))
		require.NoError(t, os.WriteFile(diff, encodePacked(t, diffPacked(wantPacked, packed)), 0o644))
		t.Fatalf("render differs from %s; actual: %s, diff: %s (rerun with -update if intended)", path, actual, diff)
	}
}

// diffPacked returns a packed buffer that is black wherever a and b differ.
func diffPacked(a, b []byte) []byte {
	out := make([]byte, len(a))
	for i := range a {
		out[i] = ^(a[i] ^ b[i])
	}
	return out
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
