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

func TestFetchEventsIcal_TimedAndAllDay(t *testing.T) {
	t.Parallel()
	srv := icalServer(t, icsFixture)
	t.Cleanup(srv.Close)

	loc, err := time.LoadLocation("America/Los_Angeles")
	require.NoError(t, err)

	events, err := calendar.FetchEventsIcal(context.Background(), srv.URL, loc)
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
	srv := icalServer(t, icsWithTZID)
	t.Cleanup(srv.Close)

	loc, err := time.LoadLocation("America/Los_Angeles")
	require.NoError(t, err)

	events, err := calendar.FetchEventsIcal(context.Background(), srv.URL, loc)
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
