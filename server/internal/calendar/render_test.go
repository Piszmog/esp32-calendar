package calendar_test

import (
	"strings"
	"testing"
	"time"

	"calendar-display/internal/calendar"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRSSIToBars(t *testing.T) {
	t.Parallel()
	cases := []struct {
		rssi int
		want int
	}{
		{0, 0},   // clamped positive / unknown sentinel → 0 bars
		{1, 0},   // rssiUnknown sentinel → 0 bars
		{999, 0}, // large positive (invalid) → 0 bars
		{-50, 4},
		{-55, 4},
		{-56, 3},
		{-65, 3},
		{-66, 2},
		{-75, 2},
		{-76, 1},
		{-85, 1},
		{-86, 0},
		{-100, 0},
	}
	for _, tc := range cases {
		t.Run("", func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, calendar.RSSIToBars(tc.rssi))
		})
	}
}

func TestTruncate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		s    string
		n    int
		want string
	}{
		{testTitleHello, 10, testTitleHello},
		{testTitleHello, 5, testTitleHello},
		{testTitleHello, 4, "hel…"},
		{testTitleHello, 1, "…"},
		{testTitleHello, 0, ""},
		{"héllo", 4, "hél…"},
		{"日本語テスト", 4, "日本語…"},
	}
	for _, tc := range cases {
		t.Run("", func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, calendar.Truncate(tc.s, tc.n))
		})
	}
}

func TestChipTimeString(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		ev   calendar.Event
		want string
	}{
		{
			"all-day",
			calendar.Event{AllDay: true, Start: time.Date(2026, 5, 11, 0, 0, 0, 0, time.UTC)},
			"all-day",
		},
		{
			"timed",
			calendar.Event{Start: time.Date(2026, 5, 11, 9, 30, 0, 0, time.UTC)},
			"09:30",
		},
		{
			"midnight",
			calendar.Event{Start: time.Date(2026, 5, 11, 0, 0, 0, 0, time.UTC)},
			"00:00",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, calendar.ChipTimeString(tc.ev))
		})
	}
}

func TestSummarizeDay(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 11, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name        string
		events      []calendar.Event
		wantSummary string
		wantMore    string
	}{
		{"empty", nil, "", ""},
		{
			"single timed",
			[]calendar.Event{{Start: time.Date(2026, 5, 11, 10, 30, 0, 0, time.UTC), Title: "Sync"}},
			"10:30  Sync", "",
		},
		{
			"single all-day",
			[]calendar.Event{{AllDay: true, Start: now, Title: testTitleHoliday}},
			testTitleHoliday, "",
		},
		{
			"multi colon-zero stripped",
			[]calendar.Event{
				{Start: time.Date(2026, 5, 11, 9, 0, 0, 0, time.UTC), Title: "Standup"},
				{Start: time.Date(2026, 5, 11, 15, 0, 0, 0, time.UTC), Title: "Review"},
			},
			"9 Standup · 15 Review", "",
		},
		{
			"multi leading zero stripped",
			[]calendar.Event{
				{Start: time.Date(2026, 5, 11, 8, 0, 0, 0, time.UTC), Title: "Alpha"},
				{Start: time.Date(2026, 5, 11, 14, 0, 0, 0, time.UTC), Title: "Beta"},
			},
			"8 Alpha · 14 Beta", "",
		},
		{
			"title truncated at 10",
			[]calendar.Event{
				{Start: time.Date(2026, 5, 11, 9, 0, 0, 0, time.UTC), Title: "Abcdefghijk"},
				{Start: time.Date(2026, 5, 11, 15, 0, 0, 0, time.UTC), Title: "X"},
			},
			"9 Abcdefghi… · 15 X", "",
		},
		{
			"all-day in multi",
			[]calendar.Event{
				{AllDay: true, Start: now, Title: "Offsite"},
				{Start: time.Date(2026, 5, 11, 10, 0, 0, 0, time.UTC), Title: "Sync"},
			},
			"Offsite · 10 Sync", "",
		},
		{
			// 4 events at 9,10,11,12 with 10-char titles = exactly 60 runes; all fit.
			"boundary all fit",
			[]calendar.Event{
				{Start: time.Date(2026, 5, 11, 9, 0, 0, 0, time.UTC), Title: testTitleAbcdefghij},
				{Start: time.Date(2026, 5, 11, 10, 0, 0, 0, time.UTC), Title: testTitleAbcdefghij},
				{Start: time.Date(2026, 5, 11, 11, 0, 0, 0, time.UTC), Title: testTitleAbcdefghij},
				{Start: time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC), Title: testTitleAbcdefghij},
			},
			// "9 Abcdefghij"(12) · "10 Abcdefghij"(13) · "11 Abcdefghij"(13) · "12 Abcdefghij"(13) = 60 runes
			"9 Abcdefghij · 10 Abcdefghij · 11 Abcdefghij · 12 Abcdefghij", "",
		},
		{
			// Adding a 5th event at 13:00 pushes the 4-joined to 61 runes (overflow);
			// 3-joined = 44 runes, leaving 1 event for the suffix → singular form.
			"overflow singular",
			[]calendar.Event{
				{Start: time.Date(2026, 5, 11, 9, 0, 0, 0, time.UTC), Title: testTitleAbcdefghij},
				{Start: time.Date(2026, 5, 11, 10, 0, 0, 0, time.UTC), Title: testTitleAbcdefghij},
				{Start: time.Date(2026, 5, 11, 11, 0, 0, 0, time.UTC), Title: testTitleAbcdefghij},
				{Start: time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC), Title: testTitleAbcdefghij},
				{Start: time.Date(2026, 5, 11, 13, 0, 0, 0, time.UTC), Title: testTitleAbcdefghij},
			},
			"9 Abcdefghij · 10 Abcdefghij · 11 Abcdefghij · 12 Abcdefghij", "+ 1 more event",
		},
		{
			// 5 events at 10..14 with 10-char titles; k=4 joined = 61 runes (overflows),
			// k=3 = 45 runes (fits) → 2 events dropped → plural suffix.
			"overflow plural",
			[]calendar.Event{
				{Start: time.Date(2026, 5, 11, 10, 0, 0, 0, time.UTC), Title: testTitleAbcdefghij},
				{Start: time.Date(2026, 5, 11, 11, 0, 0, 0, time.UTC), Title: testTitleAbcdefghij},
				{Start: time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC), Title: testTitleAbcdefghij},
				{Start: time.Date(2026, 5, 11, 13, 0, 0, 0, time.UTC), Title: testTitleAbcdefghij},
				{Start: time.Date(2026, 5, 11, 14, 0, 0, 0, time.UTC), Title: testTitleAbcdefghij},
			},
			"10 Abcdefghij · 11 Abcdefghij · 12 Abcdefghij", "+ 2 more events",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gotSummary, gotMore := calendar.SummarizeDay(tc.events)
			assert.Equal(t, tc.wantSummary, gotSummary)
			assert.Equal(t, tc.wantMore, gotMore)
		})
	}
}

func TestBuildDisplayData_PastEventCutoff(t *testing.T) {
	t.Parallel()
	loc := time.UTC
	now := time.Date(2026, 5, 11, 12, 0, 0, 0, loc)

	events := []calendar.Event{
		// 29 min ago — within cutoff, should appear in Today
		{Start: now.Add(-29 * time.Minute), Title: "Recent", AllDay: false},
		// 31 min ago — outside cutoff, should be hidden
		{Start: now.Add(-31 * time.Minute), Title: "Old", AllDay: false},
		// All-day today — should always appear regardless of Start time
		{Start: time.Date(2026, 5, 11, 0, 0, 0, 0, loc), Title: testTitleHoliday, AllDay: true},
	}

	d := calendar.BuildDisplayData(events, loc, -1, 0, now)
	titles := make([]string, 0, len(d.Today))
	for _, ev := range d.Today {
		titles = append(titles, ev.Title)
	}
	assert.Contains(t, titles, "Recent")
	assert.Contains(t, titles, testTitleHoliday)
	assert.NotContains(t, titles, "Old")
}

func TestBuildDisplayData_OngoingEventStaysVisible(t *testing.T) {
	t.Parallel()
	loc := time.UTC
	now := time.Date(2026, 5, 11, 12, 0, 0, 0, loc)

	events := []calendar.Event{
		// Started 2h ago, ends in 1h — still running, must stay in Today.
		{Start: now.Add(-2 * time.Hour), End: now.Add(time.Hour), Title: "Workshop"},
		// Started 2h ago, ended 5 min ago — over, must be hidden.
		{Start: now.Add(-2 * time.Hour), End: now.Add(-5 * time.Minute), Title: "Finished"},
	}

	d := calendar.BuildDisplayData(events, loc, -1, 0, now)
	require.Len(t, d.Today, 1)
	assert.Equal(t, "Workshop", d.Today[0].Title)
}

func TestBuildDisplayData_OvernightTimedEvent(t *testing.T) {
	t.Parallel()
	loc := time.UTC
	now := time.Date(2026, 5, 11, 12, 0, 0, 0, loc)

	// Tomorrow 22:00 → day-after 02:00: shows in Tomorrow and on day 2.
	overnight := calendar.Event{
		Start: time.Date(2026, 5, 12, 22, 0, 0, 0, loc),
		End:   time.Date(2026, 5, 13, 2, 0, 0, 0, loc),
		Title: "Red-eye",
	}

	d := calendar.BuildDisplayData([]calendar.Event{overnight}, loc, -1, 0, now)
	require.Len(t, d.Tomorrow, 1)
	assert.Equal(t, "22:00", calendar.ChipTimeString(d.Tomorrow[0]), "start day keeps its start time")
	assert.Contains(t, d.WeekAhead[0].Summary, "Red-eye", "continuation day must show the event")
	assert.NotContains(t, d.WeekAhead[0].Summary, "22:00", "continuation day must not repeat the start time")
	assert.Empty(t, d.WeekAhead[1].Summary)
}

func TestBuildDisplayData_MultiDayTimedEvent(t *testing.T) {
	t.Parallel()
	loc := time.UTC
	now := time.Date(2026, 5, 11, 12, 0, 0, 0, loc)

	// Yesterday 09:00 → tomorrow 17:00: today and tomorrow are continuation days.
	trip := calendar.Event{
		Start: time.Date(2026, 5, 10, 9, 0, 0, 0, loc),
		End:   time.Date(2026, 5, 12, 17, 0, 0, 0, loc),
		Title: "Trip",
	}
	// Ends exactly at midnight: must not leak onto the next day.
	late := calendar.Event{
		Start: time.Date(2026, 5, 13, 20, 0, 0, 0, loc),
		End:   time.Date(2026, 5, 14, 0, 0, 0, 0, loc),
		Title: "Late show",
	}

	d := calendar.BuildDisplayData([]calendar.Event{trip, late}, loc, -1, 0, now)
	require.Len(t, d.Today, 1)
	assert.Equal(t, "all-day", calendar.ChipTimeString(d.Today[0]))
	require.Len(t, d.Tomorrow, 1)
	assert.Equal(t, "all-day", calendar.ChipTimeString(d.Tomorrow[0]))
	assert.Contains(t, d.WeekAhead[0].Summary, "Late show")
	assert.Empty(t, d.WeekAhead[1].Summary, "event ending at midnight must not appear the next day")
}

func TestBuildDisplayData_DayBuckets(t *testing.T) {
	t.Parallel()
	loc := time.UTC
	now := time.Date(2026, 5, 11, 12, 0, 0, 0, loc)

	events := []calendar.Event{
		{Start: now.Add(2 * time.Hour), Title: "Today"},
		{Start: time.Date(2026, 5, 12, 10, 0, 0, 0, loc), Title: testTitleTomorrow},
		{Start: time.Date(2026, 5, 13, 10, 0, 0, 0, loc), Title: "Day2"},
		{Start: time.Date(2026, 5, 17, 10, 0, 0, 0, loc), Title: "Day6"},
		{Start: time.Date(2026, 5, 18, 10, 0, 0, 0, loc), Title: "Day7TooFar"},
	}

	d := calendar.BuildDisplayData(events, loc, -1, 0, now)

	require.Len(t, d.Today, 1)
	assert.Equal(t, "Today", d.Today[0].Title)

	require.Len(t, d.Tomorrow, 1)
	assert.Equal(t, testTitleTomorrow, d.Tomorrow[0].Title)

	require.Len(t, d.WeekAhead, 5)

	// Day+2 (index 0) should have "Day2"; Day+7 must not appear.
	var sb strings.Builder
	for _, ds := range d.WeekAhead {
		sb.WriteString(ds.Summary)
	}
	allSummaries := sb.String()
	assert.Contains(t, allSummaries, "Day2")
	assert.Contains(t, allSummaries, "Day6")
	assert.NotContains(t, allSummaries, "Day7TooFar")
}

func TestBuildDisplayData_WeekAheadSort(t *testing.T) {
	t.Parallel()
	loc := time.UTC
	now := time.Date(2026, 5, 11, 12, 0, 0, 0, loc)

	// Two events on day+3 in reverse order.
	events := []calendar.Event{
		{Start: time.Date(2026, 5, 14, 15, 0, 0, 0, loc), Title: testTitleLater},
		{Start: time.Date(2026, 5, 14, 9, 0, 0, 0, loc), Title: testTitleEarlier},
	}

	d := calendar.BuildDisplayData(events, loc, -1, 0, now)
	require.Len(t, d.WeekAhead, 5)
	// day+2 is index 0, day+3 is index 1.
	assert.Equal(t, "9 Earlier · 15 Later", d.WeekAhead[1].Summary)
}

func TestBuildDisplayData_BatteryAndWifi(t *testing.T) {
	t.Parallel()
	loc := time.UTC
	now := time.Date(2026, 5, 11, 12, 0, 0, 0, loc)

	d := calendar.BuildDisplayData(nil, loc, -1, 0, now)
	assert.Equal(t, -1, d.BatteryPct)
	assert.Equal(t, 0, d.WifiSignal)

	d2 := calendar.BuildDisplayData(nil, loc, 87, -55, now)
	assert.Equal(t, 87, d2.BatteryPct)
	assert.Equal(t, calendar.RSSIToBars(-55), d2.WifiSignal)
}

func TestRenderImage_Dimensions(t *testing.T) {
	t.Parallel()
	loc := time.UTC
	now := time.Date(2026, 5, 11, 12, 0, 0, 0, loc)
	d := calendar.BuildDisplayData(nil, loc, 87, -55, now)
	img := calendar.RenderImage(d)
	b := img.Bounds()
	assert.Equal(t, 800, b.Dx())
	assert.Equal(t, 480, b.Dy())
	// Top-left background pixel should be white.
	r, g, bl, _ := img.At(0, 0).RGBA()
	assert.Greater(t, (r+g+bl)/3, uint32(32768), "top-left pixel should be white")
}

func TestBuildDisplayData_EmptyEvents(t *testing.T) {
	t.Parallel()
	loc := time.UTC
	now := time.Date(2026, 5, 11, 12, 0, 0, 0, loc)
	d := calendar.BuildDisplayData(nil, loc, -1, 0, now)
	assert.Nil(t, d.Today)
	assert.Nil(t, d.Tomorrow)
	require.Len(t, d.WeekAhead, 5, "WeekAhead always has 5 entries regardless of events")
	for _, ds := range d.WeekAhead {
		assert.Empty(t, ds.Summary)
	}
}

func TestBuildDisplayData_TodaySort(t *testing.T) {
	t.Parallel()
	loc := time.UTC
	now := time.Date(2026, 5, 11, 8, 0, 0, 0, loc)
	// Events in reverse order, plus an all-day event, to verify Today is
	// sorted ascending by Start with the all-day event first.
	events := []calendar.Event{
		{Start: time.Date(2026, 5, 11, 15, 0, 0, 0, loc), Title: testTitleLater},
		{Start: time.Date(2026, 5, 11, 9, 0, 0, 0, loc), Title: testTitleEarlier},
		{Start: time.Date(2026, 5, 11, 0, 0, 0, 0, loc), Title: "All Day", AllDay: true},
	}
	d := calendar.BuildDisplayData(events, loc, -1, 0, now)
	require.Len(t, d.Today, 3)
	assert.Equal(t, "All Day", d.Today[0].Title)
	assert.Equal(t, testTitleEarlier, d.Today[1].Title)
	assert.Equal(t, testTitleLater, d.Today[2].Title)
}

func TestBuildDisplayData_TomorrowSort(t *testing.T) {
	t.Parallel()
	loc := time.UTC
	now := time.Date(2026, 5, 11, 12, 0, 0, 0, loc)
	// Events in reverse order to verify Tomorrow is sorted ascending by Start.
	events := []calendar.Event{
		{Start: time.Date(2026, 5, 12, 15, 0, 0, 0, loc), Title: testTitleLater},
		{Start: time.Date(2026, 5, 12, 9, 0, 0, 0, loc), Title: testTitleEarlier},
	}
	d := calendar.BuildDisplayData(events, loc, -1, 0, now)
	require.Len(t, d.Tomorrow, 2)
	assert.True(t, d.Tomorrow[0].Start.Before(d.Tomorrow[1].Start))
	assert.Equal(t, testTitleEarlier, d.Tomorrow[0].Title)
	assert.Equal(t, testTitleLater, d.Tomorrow[1].Title)
}

func TestBuildDisplayData_NowField(t *testing.T) {
	t.Parallel()
	// d.Now must equal now expressed in loc, not in the input timezone.
	loc := time.FixedZone("UTC+5", 5*3600)
	now := time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC)
	d := calendar.BuildDisplayData(nil, loc, -1, 0, now)
	assert.True(t, d.Now.Equal(now.In(loc)))
	assert.Equal(t, loc, d.Now.Location())
}

func TestBuildDisplayData_DST(t *testing.T) {
	t.Parallel()
	la, err := time.LoadLocation("America/Los_Angeles")
	require.NoError(t, err)

	// now = 2026-03-08 23:30 PST (spring-forward is the following morning).
	// An event at 2026-03-09 09:00 PDT is "tomorrow" by calendar day.
	// The old Hours/24 formula would place it in Today (daysAhead=0).
	now := time.Date(2026, 3, 8, 23, 30, 0, 0, la)
	tomorrowEvent := calendar.Event{
		Start: time.Date(2026, 3, 9, 9, 0, 0, 0, la),
		Title: "Spring Meeting",
	}

	d := calendar.BuildDisplayData([]calendar.Event{tomorrowEvent}, la, -1, 0, now)

	titles := make([]string, 0, len(d.Today))
	for _, ev := range d.Today {
		titles = append(titles, ev.Title)
	}
	tomorrowTitles := make([]string, 0, len(d.Tomorrow))
	for _, ev := range d.Tomorrow {
		tomorrowTitles = append(tomorrowTitles, ev.Title)
	}
	assert.NotContains(t, titles, "Spring Meeting", "event on March 9 must not appear in Today on March 8")
	assert.Contains(t, tomorrowTitles, "Spring Meeting", "event on March 9 must appear in Tomorrow on March 8")
}

func TestBuildDisplayData_MultiDayAllDay(t *testing.T) {
	t.Parallel()
	loc := time.UTC
	// "now" is May 11 at noon.
	now := time.Date(2026, 5, 11, 12, 0, 0, 0, loc)

	// 3-day all-day event: May 10 → May 13 (End is exclusive: spans May 10, 11, 12).
	multiDay := calendar.Event{
		Start:  time.Date(2026, 5, 10, 0, 0, 0, 0, loc),
		End:    time.Date(2026, 5, 13, 0, 0, 0, 0, loc),
		Title:  "Company Offsite",
		AllDay: true,
	}

	d := calendar.BuildDisplayData([]calendar.Event{multiDay}, loc, -1, 0, now)

	todayTitles := make([]string, 0, len(d.Today))
	for _, ev := range d.Today {
		todayTitles = append(todayTitles, ev.Title)
	}
	tomorrowTitles := make([]string, 0, len(d.Tomorrow))
	for _, ev := range d.Tomorrow {
		tomorrowTitles = append(tomorrowTitles, ev.Title)
	}
	assert.Contains(t, todayTitles, "Company Offsite", "multi-day all-day spanning today must appear in Today")
	assert.Contains(t, tomorrowTitles, "Company Offsite", "multi-day all-day spanning tomorrow must appear in Tomorrow")
}

func TestBuildDisplayData_AllDayWeekAheadSpan(t *testing.T) {
	t.Parallel()
	loc := time.UTC
	// now = May 11 (Monday). A week-long event May 11→May 18 spans all 5 week-ahead days.
	now := time.Date(2026, 5, 11, 12, 0, 0, 0, loc)
	weekEvent := calendar.Event{
		Start:  time.Date(2026, 5, 11, 0, 0, 0, 0, loc),
		End:    time.Date(2026, 5, 18, 0, 0, 0, 0, loc),
		Title:  "Week Sprint",
		AllDay: true,
	}

	d := calendar.BuildDisplayData([]calendar.Event{weekEvent}, loc, -1, 0, now)

	for i, ds := range d.WeekAhead {
		assert.Contains(t, ds.Summary, "Week Sprint", "WeekAhead[%d] (%s) should show the ongoing all-day event", i, ds.Date.Format("Mon"))
	}
}

func TestLeftColumnRows(t *testing.T) {
	t.Parallel()
	// 7 rows: the last Tomorrow row ends at y=436, above the footer at 448.
	assert.Equal(t, 7, calendar.LeftColumnRows())
}

func TestSplitLeftRows(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name                    string
		nToday, nTomorrow       int
		wantToday, wantTomorrow int
	}{
		{"both empty", 0, 0, 1, 6},
		{"few events", 2, 1, 2, 5},
		{"today overflows, tomorrow empty", 10, 0, 6, 1},
		{"today overflows, tomorrow one", 10, 1, 6, 1},
		{"today overflows, tomorrow many", 10, 5, 5, 2},
		{"tomorrow overflows", 1, 10, 1, 6},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			today, tomorrow := calendar.SplitLeftRows(tc.nToday, tc.nTomorrow)
			assert.Equal(t, tc.wantToday, today, "today rows")
			assert.Equal(t, tc.wantTomorrow, tomorrow, "tomorrow rows")
			assert.Equal(t, calendar.LeftColumnRows(), today+tomorrow)
		})
	}
}

func TestVisibleChips(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name                  string
		n, rows               int
		wantShown, wantHidden int
	}{
		{"none", 0, 3, 0, 0},
		{"fits exactly", 3, 3, 3, 0},
		{"one over", 4, 3, 2, 2},
		{"many over", 10, 5, 4, 6},
		{"single row", 2, 1, 0, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			shown, hidden := calendar.VisibleChips(tc.n, tc.rows)
			assert.Equal(t, tc.wantShown, shown, "shown")
			assert.Equal(t, tc.wantHidden, hidden, "hidden")
		})
	}
}

// TestRenderImage_BusyDayStaysAboveFooter renders more events than fit and
// checks the strip just above the footer line is blank in the left column.
func TestRenderImage_BusyDayStaysAboveFooter(t *testing.T) {
	t.Parallel()
	loc := time.UTC
	now := time.Date(2026, 5, 11, 8, 0, 0, 0, loc)
	events := make([]calendar.Event, 0, 15)
	for i := range 10 {
		events = append(events, calendar.Event{Start: now.Add(time.Duration(i+1) * time.Hour), Title: "Today event"})
	}
	for i := range 5 {
		events = append(events, calendar.Event{Start: time.Date(2026, 5, 12, 9+i, 0, 0, 0, loc), Title: testTitleTomorrow})
	}
	d := calendar.BuildDisplayData(events, loc, -1, 0, now)
	require.Len(t, d.Today, 10)
	require.Len(t, d.Tomorrow, 5)

	img := calendar.RenderImage(d)
	for y := 438; y <= 446; y++ {
		for x := 20; x <= 420; x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			require.Greater(t, (r+g+b)/3, uint32(32768), "pixel (%d,%d) above footer should be white", x, y)
		}
	}
}

func TestRenderImage_FooterShowsFetchTimeAndStale(t *testing.T) {
	t.Parallel()
	loc := time.UTC
	now := time.Date(2026, 5, 11, 12, 0, 0, 0, loc)
	base := calendar.BuildDisplayData(nil, loc, -1, 0, now)
	fresh := calendar.RenderImage(base)

	fetched := base
	fetched.FetchedAt = now.Add(-2 * time.Hour)
	older := calendar.RenderImage(fetched)

	stale := fetched
	stale.Stale = true
	staleImg := calendar.RenderImage(stale)

	assert.NotEqual(t, calendar.Pack1Bit(fresh), calendar.Pack1Bit(older), "footer should show fetch time, not render time")
	assert.NotEqual(t, calendar.Pack1Bit(older), calendar.Pack1Bit(staleImg), "stale flag should change the footer")
}
