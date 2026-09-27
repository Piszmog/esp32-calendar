package calendar_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"calendar-display/internal/calendar"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// icsFixture is a minimal .ics feed with two events:
//   - a timed event in UTC
//   - an all-day event
const icsFixture = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Test//Test//EN
BEGIN:VEVENT
UID:timed-utc@test
SUMMARY:Team standup
DTSTART:20260610T150000Z
DTEND:20260610T153000Z
END:VEVENT
BEGIN:VEVENT
UID:allday@test
SUMMARY:Conference Day
DTSTART;VALUE=DATE:20260611
DTEND;VALUE=DATE:20260612
END:VEVENT
END:VCALENDAR`

// icsWithTZID is a .ics feed with a TZID-qualified datetime.
const icsWithTZID = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Test//Test//EN
BEGIN:VEVENT
UID:tzid@test
SUMMARY:Local meeting
DTSTART;TZID=America/Los_Angeles:20260610T080000
DTEND;TZID=America/Los_Angeles:20260610T090000
END:VEVENT
END:VCALENDAR`

func icalServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/calendar")
		_, _ = w.Write([]byte(body))
	}))
}

// icsWindow is a fixed window that includes all icsFixture / icsWithTZID dates.
var (
	icsWindowMin = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	icsWindowMax = time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)
)

func TestFetchEventsIcal_TimedAndAllDay(t *testing.T) {
	t.Parallel()

	loc, err := time.LoadLocation("America/Los_Angeles")
	require.NoError(t, err)

	events, err := calendar.EventsFromICS(icsFixture, loc, icsWindowMin, icsWindowMax)
	require.NoError(t, err)

	// Filter to only the two fixture events by title.
	var standup, conf calendar.Event
	for _, e := range events {
		switch e.Title {
		case "Team standup":
			standup = e
		case "Conference Day":
			conf = e
		}
	}

	require.Equal(t, "Team standup", standup.Title)
	assert.False(t, standup.AllDay)
	assert.Equal(t, 15, standup.Start.UTC().Hour())

	require.Equal(t, "Conference Day", conf.Title)
	assert.True(t, conf.AllDay)
}

func TestFetchEventsIcal_TZID(t *testing.T) {
	t.Parallel()

	loc, err := time.LoadLocation("America/Los_Angeles")
	require.NoError(t, err)

	events, err := calendar.EventsFromICS(icsWithTZID, loc, icsWindowMin, icsWindowMax)
	require.NoError(t, err)

	var meeting calendar.Event
	for _, e := range events {
		if e.Title == "Local meeting" {
			meeting = e
		}
	}
	require.Equal(t, "Local meeting", meeting.Title)
	assert.False(t, meeting.AllDay)
	// 08:00 America/Los_Angeles
	assert.Equal(t, 8, meeting.Start.Hour())
}

// TestFetchEventsIcal_HTTP verifies the HTTP fetch path parses a valid feed
// without error (content assertions are handled in the ICS-level tests above).
func TestFetchEventsIcal_HTTP(t *testing.T) {
	t.Parallel()
	srv := icalServer(t, icsFixture)
	t.Cleanup(srv.Close)

	_, err := calendar.FetchEventsIcal(context.Background(), srv.URL, time.UTC)
	require.NoError(t, err)
}

func TestFetchEventsIcal_HTTP404(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	loc := time.UTC
	_, err := calendar.FetchEventsIcal(context.Background(), srv.URL, loc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")
}

func TestFetchEventsIcal_FilterWindow(t *testing.T) {
	t.Parallel()

	// Event that starts 48 hours in the past — should be filtered out.
	pastEvent := `BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VEVENT
UID:past@test
SUMMARY:Past event
DTSTART:19991231T120000Z
DTEND:19991231T130000Z
END:VEVENT
END:VCALENDAR`

	srv := icalServer(t, pastEvent)
	t.Cleanup(srv.Close)

	events, err := calendar.FetchEventsIcal(context.Background(), srv.URL, time.UTC)
	require.NoError(t, err)
	for _, e := range events {
		assert.NotEqual(t, "Past event", e.Title)
	}
}

// --- recurrence expansion tests ---

// anchor is a fixed Monday used across recurrence test fixtures.
// 2026-06-01 00:00:00 UTC is a Monday.
var anchor = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

func eventsFromICS(t *testing.T, body string, tMin, tMax time.Time) []calendar.Event {
	t.Helper()
	events, err := calendar.EventsFromICS(body, time.UTC, tMin, tMax)
	require.NoError(t, err)
	return events
}

func eventTitles(events []calendar.Event) []string {
	seen := make(map[string]int)
	for _, e := range events {
		seen[e.Title]++
	}
	out := make([]string, 0, len(seen))
	for title := range seen {
		out = append(out, title)
	}
	return out
}

func countTitle(events []calendar.Event, title string) int {
	n := 0
	for _, e := range events {
		if e.Title == title {
			n++
		}
	}
	return n
}

// TestExpandRecurring_WeeklyRRule verifies that a weekly recurring event
// is expanded into multiple instances within the query window.
func TestExpandRecurring_WeeklyRRule(t *testing.T) {
	t.Parallel()

	// DTSTART: 2026-06-01 10:00 UTC (Monday), RRULE repeats weekly for 4 weeks.
	body := `BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VEVENT
UID:weekly@test
SUMMARY:Weekly standup
DTSTART:20260601T100000Z
DTEND:20260601T103000Z
RRULE:FREQ=WEEKLY;COUNT=4
END:VEVENT
END:VCALENDAR`

	// Window covers all 4 occurrences: 2026-06-01, 06-08, 06-15, 06-22.
	tMin := anchor
	tMax := anchor.AddDate(0, 0, 30)
	events := eventsFromICS(t, body, tMin, tMax)

	count := countTitle(events, "Weekly standup")
	assert.Equal(t, 4, count, "expected 4 weekly occurrences in window")

	// Each instance should preserve the correct time-of-day (10:00 UTC).
	for _, e := range events {
		if e.Title == "Weekly standup" {
			assert.Equal(t, 10, e.Start.UTC().Hour())
			assert.Equal(t, 30*time.Minute, e.End.Sub(e.Start), "duration must be preserved")
		}
	}
}

// TestExpandRecurring_RRuleStartsBeforeWindow verifies that a recurring event
// whose DTSTART is before timeMin but whose recurrences fall inside the window
// are still returned — this is the exact production bug.
func TestExpandRecurring_RRuleStartsBeforeWindow(t *testing.T) {
	t.Parallel()

	// DTSTART: 2026-05-04 (one month before anchor). Repeats weekly, 8 times.
	// Occurrences 2026-06-01 and 2026-06-08 fall inside the window.
	body := `BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VEVENT
UID:stale-start@test
SUMMARY:Recurring from past
DTSTART:20260504T090000Z
DTEND:20260504T093000Z
RRULE:FREQ=WEEKLY;COUNT=8
END:VEVENT
END:VCALENDAR`

	tMin := anchor                   // 2026-06-01
	tMax := anchor.AddDate(0, 0, 14) // 2026-06-15
	events := eventsFromICS(t, body, tMin, tMax)

	count := countTitle(events, "Recurring from past")
	assert.Equal(t, 2, count, "expected 2 occurrences whose DTSTART pre-dates the window")
}

// TestExpandRecurring_ExDate verifies that EXDATE exclusions are honoured.
func TestExpandRecurring_ExDate(t *testing.T) {
	t.Parallel()

	// Weekly standup for 4 weeks; 2026-06-08 is excluded.
	body := `BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VEVENT
UID:exdate@test
SUMMARY:Standup with skip
DTSTART:20260601T100000Z
DTEND:20260601T103000Z
RRULE:FREQ=WEEKLY;COUNT=4
EXDATE:20260608T100000Z
END:VEVENT
END:VCALENDAR`

	tMin := anchor
	tMax := anchor.AddDate(0, 0, 30)
	events := eventsFromICS(t, body, tMin, tMax)

	count := countTitle(events, "Standup with skip")
	assert.Equal(t, 3, count, "2026-06-08 must be excluded by EXDATE")

	// Confirm the skipped date is absent.
	skipped := time.Date(2026, 6, 8, 10, 0, 0, 0, time.UTC)
	for _, e := range events {
		if e.Title == "Standup with skip" {
			assert.NotEqual(t, skipped, e.Start.UTC(), "EXDATE occurrence must not appear")
		}
	}
}

// TestExpandRecurring_AllDayRRule verifies that recurring all-day events
// are expanded and preserve the AllDay flag.
func TestExpandRecurring_AllDayRRule(t *testing.T) {
	t.Parallel()

	// All-day event, weekly, 3 occurrences.
	body := `BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VEVENT
UID:allday-recur@test
SUMMARY:Weekly review
DTSTART;VALUE=DATE:20260601
DTEND;VALUE=DATE:20260602
RRULE:FREQ=WEEKLY;COUNT=3
END:VEVENT
END:VCALENDAR`

	tMin := anchor
	tMax := anchor.AddDate(0, 0, 21)
	events := eventsFromICS(t, body, tMin, tMax)

	count := countTitle(events, "Weekly review")
	assert.Equal(t, 3, count, "expected 3 all-day occurrences")
	for _, e := range events {
		if e.Title == "Weekly review" {
			assert.True(t, e.AllDay, "recurring all-day events must keep AllDay=true")
		}
	}
}

// TestExpandRecurring_NonRecurringUnchanged verifies that an event without an
// RRULE still yields exactly one instance (regression guard).
func TestExpandRecurring_NonRecurringUnchanged(t *testing.T) {
	t.Parallel()

	body := `BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VEVENT
UID:onetime@test
SUMMARY:One-off meeting
DTSTART:20260602T140000Z
DTEND:20260602T150000Z
END:VEVENT
END:VCALENDAR`

	tMin := anchor
	tMax := anchor.AddDate(0, 0, 14)
	events := eventsFromICS(t, body, tMin, tMax)

	count := countTitle(events, "One-off meeting")
	assert.Equal(t, 1, count, "non-recurring event must appear exactly once")
	_ = eventTitles(events) // just to reference the helper
}

// TestExpandRecurring_RecurrenceIDOverride verifies that a RECURRENCE-ID override
// VEVENT suppresses the original base-series slot and replaces it with the override.
func TestExpandRecurring_RecurrenceIDOverride(t *testing.T) {
	t.Parallel()

	// Weekly standup for 3 weeks; the June 8 occurrence was moved to 2pm.
	body := `BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VEVENT
UID:standup-override@test
SUMMARY:Weekly standup
DTSTART:20260601T100000Z
DTEND:20260601T103000Z
RRULE:FREQ=WEEKLY;COUNT=3
END:VEVENT
BEGIN:VEVENT
UID:standup-override@test
SUMMARY:Weekly standup (moved)
DTSTART:20260608T140000Z
DTEND:20260608T143000Z
RECURRENCE-ID:20260608T100000Z
END:VEVENT
END:VCALENDAR`

	tMin := anchor
	tMax := anchor.AddDate(0, 0, 21)
	events := eventsFromICS(t, body, tMin, tMax)

	// Expect 3 events: June 1 10am, June 8 2pm (override), June 15 10am.
	assert.Len(t, events, 3, "should have 3 total events (no duplicate for June 8)")

	// The original June 8 10am slot must be excluded.
	orig := time.Date(2026, 6, 8, 10, 0, 0, 0, time.UTC)
	for _, e := range events {
		assert.False(t, e.Start.UTC().Equal(orig), "original June 8 10am slot must be suppressed by RECURRENCE-ID")
	}

	// The rescheduled June 8 2pm slot must be present.
	moved := time.Date(2026, 6, 8, 14, 0, 0, 0, time.UTC)
	found := false
	for _, e := range events {
		if e.Start.UTC().Equal(moved) {
			found = true
		}
	}
	assert.True(t, found, "rescheduled June 8 2pm override must appear")
}

// TestEventsFromCal_SkipsCancelled verifies that STATUS:CANCELLED events are
// dropped, including a cancelled override of one recurring instance (whose
// base slot must stay suppressed).
func TestEventsFromCal_SkipsCancelled(t *testing.T) {
	t.Parallel()

	body := `BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VEVENT
UID:cancelled-single@test
SUMMARY:Called off
STATUS:CANCELLED
DTSTART:20260602T100000Z
DTEND:20260602T110000Z
END:VEVENT
BEGIN:VEVENT
UID:standup-cancel@test
SUMMARY:Weekly standup
DTSTART:20260601T100000Z
DTEND:20260601T103000Z
RRULE:FREQ=WEEKLY;COUNT=3
END:VEVENT
BEGIN:VEVENT
UID:standup-cancel@test
SUMMARY:Weekly standup
STATUS:cancelled
DTSTART:20260608T100000Z
DTEND:20260608T103000Z
RECURRENCE-ID:20260608T100000Z
END:VEVENT
END:VCALENDAR`

	events := eventsFromICS(t, body, anchor, anchor.AddDate(0, 0, 21))

	assert.NotContains(t, eventTitles(events), "Called off")
	require.Len(t, events, 2, "only June 1 and June 15 standups should remain")
	cancelled := time.Date(2026, 6, 8, 10, 0, 0, 0, time.UTC)
	for _, e := range events {
		assert.False(t, e.Start.UTC().Equal(cancelled), "cancelled June 8 instance must not appear")
	}
}

// TestExpandRecurring_RRuleParseErrorFallback verifies that a malformed RRULE
// falls back to the single DTSTART occurrence instead of silently dropping the event.
func TestExpandRecurring_RRuleParseErrorFallback(t *testing.T) {
	t.Parallel()

	// RRULE with an unrecognised extension key causes StrToROption to fail.
	body := `BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VEVENT
UID:bad-rrule@test
SUMMARY:Event with bad RRULE
DTSTART:20260602T100000Z
DTEND:20260602T110000Z
RRULE:FREQ=WEEKLY;X-UNKNOWN=bad
END:VEVENT
END:VCALENDAR`

	tMin := anchor
	tMax := anchor.AddDate(0, 0, 14)
	events := eventsFromICS(t, body, tMin, tMax)

	count := countTitle(events, "Event with bad RRULE")
	assert.Equal(t, 1, count, "malformed RRULE must fall back to DTSTART occurrence, not silently drop the event")
}

// TestExpandRecurring_AllDayAcrossDST verifies that recurring all-day
// occurrences keep whole-day spans when the base event or an occurrence falls
// on a 23h or 25h DST day.
func TestExpandRecurring_AllDayAcrossDST(t *testing.T) {
	t.Parallel()
	loc, err := time.LoadLocation("America/Denver")
	require.NoError(t, err)

	cases := []struct {
		name    string
		dtstart string
		rrule   string
		from    time.Time
	}{
		// Base day is 2026-03-08, a 23h spring-forward day; 2027-03-08 is not.
		{"base on spring-forward", "20260308", "FREQ=YEARLY", time.Date(2027, 3, 7, 0, 0, 0, 0, loc)},
		// Base day is a normal 24h day; the 2026-11-01 occurrence is 25h.
		{"occurrence on fall-back", "20261025", "FREQ=WEEKLY;COUNT=3", time.Date(2026, 10, 31, 0, 0, 0, 0, loc)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			start, err := time.ParseInLocation("20060102", tc.dtstart, loc)
			require.NoError(t, err)
			body := "BEGIN:VCALENDAR\nVERSION:2.0\nBEGIN:VEVENT\nUID:dst@test\nSUMMARY:Birthday\n" +
				"DTSTART;VALUE=DATE:" + tc.dtstart + "\n" +
				"DTEND;VALUE=DATE:" + start.AddDate(0, 0, 1).Format("20060102") + "\n" +
				"RRULE:" + tc.rrule + "\nEND:VEVENT\nEND:VCALENDAR"
			events, err := calendar.EventsFromICS(body, loc, tc.from, tc.from.AddDate(0, 0, 3))
			require.NoError(t, err)
			require.Len(t, events, 1)
			ev := events[0]
			assert.Equal(t, ev.Start.AddDate(0, 0, 1), ev.End, "all-day occurrence must end at the next midnight")

			d := calendar.BuildDisplayData(events, loc, -1, 0, ev.Start.Add(12*time.Hour))
			assert.Len(t, d.Today, 1, "all-day occurrence must show on its day")
		})
	}
}

func TestFetchEventsIcal_SendsUserAgent(t *testing.T) {
	t.Parallel()

	gotUA := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA <- r.UserAgent()
		_, _ = w.Write([]byte(icsFixture))
	}))
	t.Cleanup(srv.Close)

	_, err := calendar.FetchEventsIcal(t.Context(), srv.URL, time.UTC)
	require.NoError(t, err)
	assert.Equal(t, "calendar-display", <-gotUA)
}

func TestFetchEventsIcal_TooLarge(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte("x"), 10<<20+1))
	}))
	t.Cleanup(srv.Close)

	_, err := calendar.FetchEventsIcal(t.Context(), srv.URL, time.UTC)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds size limit")
}

func TestFetchEventsIcal_ErrorOmitsURL(t *testing.T) {
	t.Parallel()

	// A closed server gives a dial error, which net/http wraps with the URL.
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()

	_, err := calendar.FetchEventsIcal(t.Context(), srv.URL+"/private-secret-token/basic.ics", time.UTC)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "secret-token")
}

// notLocal returns a fixed zone five hours ahead of time.Local, so floating
// iCal times parsed in time.Local land on the wrong instant unless they are
// re-anchored to the configured zone.
func notLocal() *time.Location {
	_, off := time.Now().Zone()
	return time.FixedZone("not-local", off+5*60*60)
}

// TestExpandRecurring_FloatingExclusionsUseConfiguredZone verifies that
// floating and all-day EXDATE / RECURRENCE-ID values are matched in the
// configured zone, not the host's time.Local.
func TestExpandRecurring_FloatingExclusionsUseConfiguredZone(t *testing.T) {
	t.Parallel()
	loc := notLocal()
	from := time.Date(2026, 6, 1, 0, 0, 0, 0, loc)

	allDay := `BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VEVENT
UID:allday-exdate@test
SUMMARY:Gym
DTSTART;VALUE=DATE:20260601
DTEND;VALUE=DATE:20260602
RRULE:FREQ=WEEKLY;COUNT=3
EXDATE;VALUE=DATE:20260608
END:VEVENT
END:VCALENDAR`
	events, err := calendar.EventsFromICS(allDay, loc, from, from.AddDate(0, 0, 21))
	require.NoError(t, err)
	assert.Equal(t, 2, countTitle(events, "Gym"), "all-day EXDATE must exclude 2026-06-08")

	floating := `BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VEVENT
UID:floating@test
SUMMARY:Sync
DTSTART:20260601T100000
DTEND:20260601T103000
RRULE:FREQ=WEEKLY;COUNT=3
END:VEVENT
BEGIN:VEVENT
UID:floating@test
RECURRENCE-ID:20260608T100000
SUMMARY:Sync moved
DTSTART:20260608T140000
DTEND:20260608T143000
END:VEVENT
END:VCALENDAR`
	events, err = calendar.EventsFromICS(floating, loc, from, from.AddDate(0, 0, 21))
	require.NoError(t, err)
	assert.Equal(t, 2, countTitle(events, "Sync"), "floating RECURRENCE-ID must suppress the base slot")
	assert.Equal(t, 1, countTitle(events, "Sync moved"))
}

// TestExpandRecurring_ExpandsInEventZone verifies that a series defined in
// another zone keeps its own wall-clock time when the two zones change DST on
// different dates.
func TestExpandRecurring_ExpandsInEventZone(t *testing.T) {
	t.Parallel()
	denver, err := time.LoadLocation("America/Denver")
	require.NoError(t, err)
	london, err := time.LoadLocation("Europe/London")
	require.NoError(t, err)

	// Weekly 09:00 London from 2026-02-02. The US springs forward on
	// 2026-03-08, the UK on 2026-03-29; 2026-03-16 falls in between.
	body := `BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VEVENT
UID:london@test
SUMMARY:London call
DTSTART;TZID=Europe/London:20260202T090000
DTEND;TZID=Europe/London:20260202T093000
RRULE:FREQ=WEEKLY
END:VEVENT
END:VCALENDAR`
	from := time.Date(2026, 3, 15, 0, 0, 0, 0, denver)
	events, err := calendar.EventsFromICS(body, denver, from, from.AddDate(0, 0, 3))
	require.NoError(t, err)
	require.Len(t, events, 1)
	ev := events[0]
	assert.Equal(t, 9, ev.Start.In(london).Hour(), "occurrence must stay at 09:00 London")
	assert.Equal(t, denver, ev.Start.Location(), "events are normalized to the configured zone")
}

func TestParseIcalDuration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in    string
		days  int
		clock time.Duration
		ok    bool
	}{
		{"P1W", 7, 0, true},
		{"P3D", 3, 0, true},
		{"PT1H30M", 0, 90 * time.Minute, true},
		{"P1DT2H", 1, 2 * time.Hour, true},
		{"+PT45S", 0, 45 * time.Second, true},
		{"-PT15M", 0, -15 * time.Minute, true},
		{"", 0, 0, false},
		{"P", 0, 0, false},
		{"PT", 0, 0, false},
		{"1H", 0, 0, false},
		{"P1WT1H", 0, 0, false},
	}
	for _, tc := range tests {
		days, clock, ok := calendar.ParseIcalDuration(tc.in)
		assert.Equal(t, tc.ok, ok, "ok for %q", tc.in)
		assert.Equal(t, tc.days, days, "days for %q", tc.in)
		assert.Equal(t, tc.clock, clock, "clock for %q", tc.in)
	}
}

// TestEventsFromCal_Duration verifies DTSTART+DURATION events get an End, so
// they stay visible while running and span the days they cover.
func TestEventsFromCal_Duration(t *testing.T) {
	t.Parallel()

	body := `BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VEVENT
UID:timed-dur@test
SUMMARY:Timed duration
DTSTART:20260602T100000Z
DURATION:PT1H30M
END:VEVENT
BEGIN:VEVENT
UID:allday-dur@test
SUMMARY:All-day duration
DTSTART;VALUE=DATE:20260603
DURATION:P3D
END:VEVENT
BEGIN:VEVENT
UID:weekly-dur@test
SUMMARY:Weekly duration
DTSTART:20260601T090000Z
DURATION:PT45M
RRULE:FREQ=WEEKLY;COUNT=2
END:VEVENT
END:VCALENDAR`
	events := eventsFromICS(t, body, anchor, anchor.AddDate(0, 0, 30))

	require.Equal(t, 1, countTitle(events, "Timed duration"))
	require.Equal(t, 1, countTitle(events, "All-day duration"))
	require.Equal(t, 2, countTitle(events, "Weekly duration"))
	for _, e := range events {
		switch e.Title {
		case "Timed duration":
			assert.Equal(t, 90*time.Minute, e.End.Sub(e.Start))
		case "All-day duration":
			assert.True(t, e.AllDay)
			assert.Equal(t, time.Date(2026, 6, 6, 0, 0, 0, 0, time.UTC), e.End)
		case "Weekly duration":
			assert.Equal(t, 45*time.Minute, e.End.Sub(e.Start))
		}
	}
}

// TestEventsFromCal_DTEndWinsOverDuration verifies DTEND is used when both
// are present (invalid per RFC 5545, but seen in the wild).
func TestEventsFromCal_DTEndWinsOverDuration(t *testing.T) {
	t.Parallel()

	body := `BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VEVENT
UID:both@test
SUMMARY:Both
DTSTART:20260602T100000Z
DTEND:20260602T110000Z
DURATION:PT3H
END:VEVENT
END:VCALENDAR`
	events := eventsFromICS(t, body, anchor, anchor.AddDate(0, 0, 30))
	require.Len(t, events, 1)
	assert.Equal(t, time.Hour, events[0].End.Sub(events[0].Start))
}

// TestEventsFromCal_TitleWhitespaceCollapsed verifies escaped newlines and
// runs of whitespace in SUMMARY render as single spaces.
func TestEventsFromCal_TitleWhitespaceCollapsed(t *testing.T) {
	t.Parallel()

	body := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:ws@test\r\n" +
		`SUMMARY:  Line one\nLine two	 end ` + "\r\n" +
		"DTSTART:20260602T100000Z\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	events := eventsFromICS(t, body, anchor, anchor.AddDate(0, 0, 30))
	require.Len(t, events, 1)
	assert.Equal(t, "Line one Line two end", events[0].Title)
}

// TestEventsFromCal_DTStartIsFirstInstance verifies DTSTART counts as an
// occurrence (RFC 5545) for RDATE-only events and when DTSTART doesn't match
// the RRULE, without duplicating it when it does match.
func TestEventsFromCal_DTStartIsFirstInstance(t *testing.T) {
	t.Parallel()

	body := `BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VEVENT
UID:rdate-only@test
SUMMARY:RDATE only
DTSTART:20260602T100000Z
DTEND:20260602T110000Z
RDATE:20260604T100000Z
END:VEVENT
BEGIN:VEVENT
UID:mismatch@test
SUMMARY:Mismatch
DTSTART:20260602T100000Z
DTEND:20260602T110000Z
RRULE:FREQ=WEEKLY;BYDAY=TH;COUNT=2
END:VEVENT
BEGIN:VEVENT
UID:match@test
SUMMARY:Match
DTSTART:20260602T100000Z
DTEND:20260602T110000Z
RRULE:FREQ=WEEKLY;COUNT=2
END:VEVENT
END:VCALENDAR`
	events := eventsFromICS(t, body, anchor, anchor.AddDate(0, 0, 30))

	dtstart := time.Date(2026, 6, 2, 10, 0, 0, 0, time.UTC)
	for _, title := range []string{"RDATE only", "Mismatch"} {
		found := false
		for _, e := range events {
			if e.Title == title && e.Start.Equal(dtstart) {
				found = true
			}
		}
		assert.True(t, found, "%s: DTSTART instance missing", title)
	}
	assert.Equal(t, 2, countTitle(events, "RDATE only"))
	assert.Equal(t, 3, countTitle(events, "Mismatch"))
	assert.Equal(t, 2, countTitle(events, "Match"))
}

// TestEventsFromCal_DropsMissingGlyphs verifies characters the embedded fonts
// can't draw (emoji) are removed from titles, while ones they can are kept.
func TestEventsFromCal_DropsMissingGlyphs(t *testing.T) {
	t.Parallel()

	body := `BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VEVENT
UID:emoji@test
SUMMARY:🎂 Mom's birthday ☀
DTSTART:20260602T100000Z
END:VEVENT
BEGIN:VEVENT
UID:accent@test
SUMMARY:Café ✓
DTSTART:20260602T110000Z
END:VEVENT
BEGIN:VEVENT
UID:only-emoji@test
SUMMARY:🎉
DTSTART:20260602T120000Z
END:VEVENT
END:VCALENDAR`
	events := eventsFromICS(t, body, anchor, anchor.AddDate(0, 0, 30))
	assert.ElementsMatch(t, []string{"Mom's birthday ☀", "Café ✓", "(no title)"}, eventTitles(events))
}

// TestEventsFromCal_SkipsDeclined verifies events the calendar owner (from
// X-WR-CALNAME) declined are dropped, including a declined override of one
// recurring instance, whose base slot must stay suppressed.
func TestEventsFromCal_SkipsDeclined(t *testing.T) {
	t.Parallel()

	body := `BEGIN:VCALENDAR
VERSION:2.0
X-WR-CALNAME:me@example.com
BEGIN:VEVENT
UID:declined@test
SUMMARY:Declined
DTSTART:20260602T100000Z
ATTENDEE;CN=me@example.com;PARTSTAT=DECLINED:MAILTO:Me@Example.com
END:VEVENT
BEGIN:VEVENT
UID:other-declined@test
SUMMARY:Someone else declined
DTSTART:20260602T110000Z
ATTENDEE;PARTSTAT=ACCEPTED:mailto:me@example.com
ATTENDEE;PARTSTAT=DECLINED:mailto:bob@example.com
END:VEVENT
BEGIN:VEVENT
UID:standup-decline@test
SUMMARY:Weekly standup
DTSTART:20260601T100000Z
DTEND:20260601T103000Z
RRULE:FREQ=WEEKLY;COUNT=3
ATTENDEE;PARTSTAT=ACCEPTED:mailto:me@example.com
END:VEVENT
BEGIN:VEVENT
UID:standup-decline@test
SUMMARY:Weekly standup
DTSTART:20260608T100000Z
DTEND:20260608T103000Z
RECURRENCE-ID:20260608T100000Z
ATTENDEE;PARTSTAT=DECLINED:mailto:me@example.com
END:VEVENT
END:VCALENDAR`
	events := eventsFromICS(t, body, anchor, anchor.AddDate(0, 0, 21))

	titles := eventTitles(events)
	assert.NotContains(t, titles, "Declined")
	assert.Contains(t, titles, "Someone else declined")
	assert.Equal(t, 2, countTitle(events, "Weekly standup"), "declined June 8 instance must not appear")
}

// TestEventsFromCal_DeclinedNeedsOwnerEmail verifies the declined filter is
// off when X-WR-CALNAME isn't an email, since the owner is then unknown.
func TestEventsFromCal_DeclinedNeedsOwnerEmail(t *testing.T) {
	t.Parallel()

	body := `BEGIN:VCALENDAR
VERSION:2.0
X-WR-CALNAME:Family
BEGIN:VEVENT
UID:declined@test
SUMMARY:Declined
DTSTART:20260602T100000Z
ATTENDEE;PARTSTAT=DECLINED:mailto:Family
END:VEVENT
END:VCALENDAR`
	events := eventsFromICS(t, body, anchor, anchor.AddDate(0, 0, 30))
	assert.Equal(t, []string{"Declined"}, eventTitles(events))
}
