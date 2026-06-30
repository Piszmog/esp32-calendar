package calendar_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"calendar-display/internal/calendar"

	ics "github.com/arran4/golang-ical"
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

// TestFetchEventsIcal_HTTP verifies the HTTP fetch path returns an event whose
// DTSTART lies within the default [now-1h, now+8d] window, exercising the
// window-computation logic in fetchEventsIcal that the ICS-level tests bypass.
func TestFetchEventsIcal_HTTP(t *testing.T) {
	t.Parallel()

	// Place a sentinel event 2 hours from now — safely inside [now-1h, now+8d].
	future := time.Now().UTC().Add(2 * time.Hour)
	fixture := fmt.Sprintf(`BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VEVENT
UID:http-window@test
SUMMARY:HTTP window sentinel
DTSTART:%s
DTEND:%s
END:VEVENT
END:VCALENDAR`, future.Format("20060102T150405Z"), future.Add(30*time.Minute).Format("20060102T150405Z"))

	srv := icalServer(t, fixture)
	t.Cleanup(srv.Close)

	events, err := calendar.FetchEventsIcal(context.Background(), srv.URL, time.UTC)
	require.NoError(t, err)

	found := false
	for _, e := range events {
		if e.Title == "HTTP window sentinel" {
			found = true
		}
	}
	assert.True(t, found, "fetchEventsIcal must include events within the default [now-1h, now+8d] window")
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

// --- floating-datetime timezone tests (findings #1, #2) ---

// TestICalPropTimes_FloatingUsesLoc verifies that icalPropTimes anchors floating
// datetimes (no TZID, no Z) in the supplied loc, not in time.Local.
// This is the unit-level proof for the RECURRENCE-ID / EXDATE fix.
func TestICalPropTimes_FloatingUsesLoc(t *testing.T) {
	t.Parallel()

	// Build a minimal IANAProperty with a floating datetime value.
	prop := &ics.IANAProperty{
		BaseProperty: ics.BaseProperty{
			IANAToken:      "EXDATE",
			Value:          "20260608T100000",
			ICalParameters: map[string][]string{},
		},
	}

	nyLoc, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)

	times := calendar.ICalPropTimes(prop, nyLoc)
	require.Len(t, times, 1)

	got := times[0]
	// Floating value "20260608T100000" parsed in America/New_York is 10:00 NY =
	// 14:00 UTC. If it were parsed in time.Local (e.g. UTC) the UTC instant
	// would be 10:00 UTC — a 4-hour difference.
	wantUTC := time.Date(2026, 6, 8, 14, 0, 0, 0, time.UTC)
	assert.True(t, got.UTC().Equal(wantUTC),
		"floating datetime must be parsed in loc (America/New_York), got UTC %s want %s",
		got.UTC(), wantUTC)
}

// TestFloatingExDateSuppression_LocalNeLoc verifies that a floating EXDATE
// suppresses its occurrence even when time.Local differs from loc.
// This is a serial integration test: it mutates time.Local, so it must NOT
// call t.Parallel().
func TestFloatingExDateSuppression_LocalNeLoc(t *testing.T) {
	// Intentionally not parallel — mutates the global time.Local.
	kolkata, err := time.LoadLocation("Asia/Kolkata") // UTC+5:30, no DST
	require.NoError(t, err)
	la, err := time.LoadLocation("America/Los_Angeles")
	require.NoError(t, err)

	orig := time.Local
	time.Local = kolkata
	defer func() { time.Local = orig }()

	// Weekly standup for 4 weeks; 2026-06-08 10:00 LA is excluded.
	// EXDATE is floating (no Z, no TZID): old code parsed it in time.Local
	// (Asia/Kolkata → 04:30 UTC instead of 17:00 UTC), so the wrong occurrence
	// was targeted and the June 8 slot was not suppressed.
	body := `BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VEVENT
UID:float-exdate@test
SUMMARY:Float EXDATE test
DTSTART;TZID=America/Los_Angeles:20260601T100000
DTEND;TZID=America/Los_Angeles:20260601T103000
RRULE:FREQ=WEEKLY;COUNT=4
EXDATE:20260608T100000
END:VEVENT
END:VCALENDAR`

	tMin := anchor
	tMax := anchor.AddDate(0, 0, 30)
	events, err := calendar.EventsFromICS(body, la, tMin, tMax)
	require.NoError(t, err)

	count := countTitle(events, "Float EXDATE test")
	assert.Equal(t, 3, count, "EXDATE must suppress the June 8 slot regardless of time.Local")

	skipped := time.Date(2026, 6, 8, 10, 0, 0, 0, la)
	for _, e := range events {
		if e.Title == "Float EXDATE test" {
			assert.False(t, e.Start.Equal(skipped), "June 8 10:00 LA must not appear")
		}
	}
}

// TestFloatingRecurrenceIDOverride_LocalNeLoc verifies that a floating
// RECURRENCE-ID suppresses the base-series slot even when time.Local != loc.
// Serial: mutates time.Local.
func TestFloatingRecurrenceIDOverride_LocalNeLoc(t *testing.T) {
	// Intentionally not parallel — mutates the global time.Local.
	kolkata, err := time.LoadLocation("Asia/Kolkata")
	require.NoError(t, err)
	la, err := time.LoadLocation("America/Los_Angeles")
	require.NoError(t, err)

	orig := time.Local
	time.Local = kolkata
	defer func() { time.Local = orig }()

	// Weekly standup; June 8 was moved to 2 pm. RECURRENCE-ID is floating
	// (no Z, no TZID): old code parsed it in time.Local (Asia/Kolkata → 04:30 UTC
	// instead of 17:00 UTC), so the ExDate didn't match the base occurrence and
	// the June 8 10:00 slot appeared alongside the 14:00 override.
	body := `BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VEVENT
UID:float-recid@test
SUMMARY:Float RECID standup
DTSTART;TZID=America/Los_Angeles:20260601T100000
DTEND;TZID=America/Los_Angeles:20260601T103000
RRULE:FREQ=WEEKLY;COUNT=3
END:VEVENT
BEGIN:VEVENT
UID:float-recid@test
SUMMARY:Float RECID standup (moved)
DTSTART;TZID=America/Los_Angeles:20260608T140000
DTEND;TZID=America/Los_Angeles:20260608T143000
RECURRENCE-ID:20260608T100000
END:VEVENT
END:VCALENDAR`

	tMin := anchor
	tMax := anchor.AddDate(0, 0, 21)
	events, err := calendar.EventsFromICS(body, la, tMin, tMax)
	require.NoError(t, err)

	assert.Len(t, events, 3, "should have 3 events (no duplicate for June 8)")

	orig10 := time.Date(2026, 6, 8, 10, 0, 0, 0, la)
	for _, e := range events {
		assert.False(t, e.Start.Equal(orig10), "original June 8 10:00 slot must be suppressed")
	}

	moved14 := time.Date(2026, 6, 8, 14, 0, 0, 0, la)
	found := false
	for _, e := range events {
		if e.Start.Equal(moved14) {
			found = true
		}
	}
	assert.True(t, found, "rescheduled June 8 14:00 override must appear")
}

// --- UNTIL timezone test (finding #3) ---

// TestRRuleUntilInLoc verifies that a non-Z UNTIL is interpreted in loc, not
// UTC. The fixture has UNTIL=20260615T095959 (LA time) which in UTC is
// 20260615T165959Z. A WEEKLY rule starting 2026-06-01 10:00 LA has occurrences
// on Jun 1, Jun 8, Jun 15. Jun 15 10:00 LA < UNTIL 09:59:59 LA is FALSE, so
// the rule must yield only 2 occurrences (Jun 1 and Jun 8). If UNTIL were
// parsed as UTC the comparison would be wrong.
func TestRRuleUntilInLoc(t *testing.T) {
	t.Parallel()

	la, err := time.LoadLocation("America/Los_Angeles")
	require.NoError(t, err)

	body := `BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VEVENT
UID:until-loc@test
SUMMARY:UNTIL test
DTSTART;TZID=America/Los_Angeles:20260601T100000
DTEND;TZID=America/Los_Angeles:20260601T103000
RRULE:FREQ=WEEKLY;UNTIL=20260615T095959
END:VEVENT
END:VCALENDAR`

	tMin := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	tMax := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)
	events, err := calendar.EventsFromICS(body, la, tMin, tMax)
	require.NoError(t, err)

	count := countTitle(events, "UNTIL test")
	assert.Equal(t, 2, count, "UNTIL in LA time excludes the Jun 15 occurrence; got %d", count)
}

// --- occurrence cap test (finding #4) ---

// TestExpandRecurring_OccurrenceCap verifies that a FREQ=SECONDLY rule with no
// COUNT/UNTIL is bounded by the cap and returns promptly.
func TestExpandRecurring_OccurrenceCap(t *testing.T) {
	t.Parallel()

	body := `BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VEVENT
UID:secondly@test
SUMMARY:Secondly event
DTSTART:20260601T000000Z
RRULE:FREQ=SECONDLY
END:VEVENT
END:VCALENDAR`

	tMin := anchor
	tMax := anchor.AddDate(0, 0, 8)
	events := eventsFromICS(t, body, tMin, tMax)

	// Must not materialise ~694 800 occurrences; cap keeps it at ≤ MaxRecurrenceOccurrences.
	assert.LessOrEqual(t, len(events), calendar.MaxRecurrenceOccurrences,
		"occurrence cap must prevent unbounded expansion")
	assert.NotEmpty(t, events, "must return at least some occurrences")
}

// --- malformed RRULE + RECURRENCE-ID override test (finding #2) ---

// TestRRuleParseFailureWithRecurrenceID verifies that when an RRULE fails to
// parse, a RECURRENCE-ID override for the same event still suppresses the base
// DTSTART occurrence. Without the fix, both the base and the override appear.
func TestRRuleParseFailureWithRecurrenceID(t *testing.T) {
	t.Parallel()

	la, err := time.LoadLocation("America/Los_Angeles")
	require.NoError(t, err)

	// The base VEVENT has a malformed RRULE so buildRRuleSet returns hasRules=false.
	// The RECURRENCE-ID sibling moves the June 1 10:00 slot to 14:00.
	body := `BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VEVENT
UID:bad-rrule-recid@test
SUMMARY:Base event
DTSTART;TZID=America/Los_Angeles:20260601T100000
DTEND;TZID=America/Los_Angeles:20260601T103000
RRULE:FREQ=BOGUS
END:VEVENT
BEGIN:VEVENT
UID:bad-rrule-recid@test
SUMMARY:Override event
DTSTART;TZID=America/Los_Angeles:20260601T140000
DTEND;TZID=America/Los_Angeles:20260601T143000
RECURRENCE-ID;TZID=America/Los_Angeles:20260601T100000
END:VEVENT
END:VCALENDAR`

	tMin := anchor
	tMax := anchor.AddDate(0, 0, 7)
	events, err := calendar.EventsFromICS(body, la, tMin, tMax)
	require.NoError(t, err)

	assert.Len(t, events, 1, "only the override must appear; base DTSTART must be suppressed")
	if len(events) == 1 {
		want := time.Date(2026, 6, 1, 14, 0, 0, 0, la)
		assert.True(t, events[0].Start.Equal(want), "the 14:00 override must be the surviving event")
	}
}

// --- ongoing-at-window-start regression test (finding #3) ---

// TestExpandRecurring_OngoingAtWindowStart guards the invariant that a recurring
// occurrence whose start is before timeMin but whose end is after timeMin is still
// included in results. The old code enforced this via queryMin widening;
// the current code relies on occurrenceInstance checking occEnd.Before(timeMin).
func TestExpandRecurring_OngoingAtWindowStart(t *testing.T) {
	t.Parallel()

	// Occurrence starts at 00:30Z, ends at 02:30Z (2h duration).
	// tMin = 01:00Z — 30min into the occurrence. Must still appear.
	body := `BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VEVENT
UID:ongoing-at-start@test
SUMMARY:Ongoing meeting
DTSTART:20260601T003000Z
DTEND:20260601T023000Z
RRULE:FREQ=WEEKLY;COUNT=2
END:VEVENT
END:VCALENDAR`

	tMin := time.Date(2026, 6, 1, 1, 0, 0, 0, time.UTC)
	tMax := time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)
	events := eventsFromICS(t, body, tMin, tMax)

	ongoing := time.Date(2026, 6, 1, 0, 30, 0, 0, time.UTC)
	found := false
	for _, e := range events {
		if e.Title == "Ongoing meeting" && e.Start.Equal(ongoing) {
			found = true
		}
	}
	assert.True(t, found, "occurrence starting at 00:30Z (before tMin 01:00Z) but ending at 02:30Z must be included")
}

// TestExpandRecurring_ExDateWithBadRRule verifies that a VEVENT with a malformed
// RRULE and an EXDATE that targets the DTSTART slot does not emit the excluded
// occurrence. The !hasRules fallback must honour EXDATEs, not just extraExdates.
func TestExpandRecurring_ExDateWithBadRRule(t *testing.T) {
	t.Parallel()

	body := `BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VEVENT
UID:bad-rrule-exdate@test
SUMMARY:Excluded meeting
DTSTART:20260602T100000Z
DTEND:20260602T110000Z
RRULE:FREQ=BOGUS
EXDATE:20260602T100000Z
END:VEVENT
END:VCALENDAR`

	tMin := anchor
	tMax := anchor.AddDate(0, 0, 14)
	events := eventsFromICS(t, body, tMin, tMax)

	for _, e := range events {
		assert.NotEqual(t, "Excluded meeting", e.Title, "EXDATE must suppress the DTSTART occurrence even when RRULE fails to parse")
	}
}
