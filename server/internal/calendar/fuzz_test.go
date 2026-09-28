package calendar_test

import (
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"calendar-display/internal/calendar"
)

const icsRecurringSeed = `BEGIN:VCALENDAR
VERSION:2.0
X-WR-CALNAME:me@example.com
BEGIN:VEVENT
UID:weekly@test
SUMMARY:Weekly
DTSTART;TZID=America/New_York:20260601T090000
DURATION:PT1H
RRULE:FREQ=WEEKLY;COUNT=4
EXDATE;TZID=America/New_York:20260608T090000
END:VEVENT
BEGIN:VEVENT
UID:weekly@test
RECURRENCE-ID;TZID=America/New_York:20260615T090000
SUMMARY:Moved
DTSTART;TZID=America/New_York:20260615T110000
DTEND;TZID=America/New_York:20260615T120000
ATTENDEE;PARTSTAT=DECLINED:mailto:me@example.com
END:VEVENT
BEGIN:VEVENT
UID:daily@test
SUMMARY:Daily
DTSTART;VALUE=DATE:20260601
RRULE:FREQ=DAILY;UNTIL=20260605
RDATE;VALUE=DATE:20260610
STATUS:CANCELLED
END:VEVENT
END:VCALENDAR`

// FuzzEventsFromICS feeds arbitrary feeds through parsing and recurrence
// expansion. The feed is remote input, so it must never panic, and every
// event it yields must be displayable.
func FuzzEventsFromICS(f *testing.F) {
	for _, seed := range []string{icsFixture, icsWithTZID, icsRecurringSeed, ""} {
		f.Add(seed)
	}
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, body string) {
		events, err := calendar.EventsFromICS(body, loc, icsWindowMin, icsWindowMax)
		if err != nil {
			return
		}
		for _, ev := range events {
			if ev.Title == "" {
				t.Errorf("event at %v has an empty title", ev.Start)
			}
			if ev.Start.Location() != loc {
				t.Errorf("event %q start in %v, want %v", ev.Title, ev.Start.Location(), loc)
			}
		}
	})
}

// FuzzStatusFromQuery checks that any query string yields values the status
// bar can draw: bat is -1 (absent) or 0..100, rssi is unknown or -120..0.
func FuzzStatusFromQuery(f *testing.F) {
	for _, seed := range []string{"", "bat=87&rssi=-55", "bat=-5", "rssi=999", "bat=x&rssi=%zz", "bat=1e3&bat=2"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, query string) {
		r := httptest.NewRequestWithContext(t.Context(), "GET", testPathBin, nil)
		r.URL.RawQuery = query
		bat, rssi := calendar.StatusFromQuery(r)
		if bat != -1 && (bat < 0 || bat > 100) {
			t.Errorf("query %q: bat = %d", url.QueryEscape(query), bat)
		}
		if rssi != calendar.RSSIUnknown && (rssi < -120 || rssi > 0) {
			t.Errorf("query %q: rssi = %d", url.QueryEscape(query), rssi)
		}
	})
}
