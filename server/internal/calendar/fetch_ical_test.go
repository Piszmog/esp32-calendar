package calendar_test

import (
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

	tMin := anchor                    // 2026-06-01
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
