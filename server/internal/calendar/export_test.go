package calendar

import (
	"context"
	"fmt"
	"image"
	"net/http"
	"strings"
	"sync"
	"time"

	ics "github.com/arran4/golang-ical"
)

// RSSIUnknown exposes rssiUnknown so tests can assert the absent-param sentinel.
const RSSIUnknown = rssiUnknown

// Event, DisplayData, DaySummary expose unexported types to the test package.
type Event = event
type DisplayData = displayData
type DaySummary = daySummary

// Pack1Bit wraps pack1Bit for blackbox tests.
func Pack1Bit(img image.Image) []byte { return pack1Bit(img) }

// BuildDisplayData wraps buildDisplayData for blackbox tests.
func BuildDisplayData(events []Event, loc *time.Location, batPct, rssi int, now time.Time) DisplayData {
	return buildDisplayData(events, loc, batPct, rssi, now)
}

// SummarizeDay wraps summarizeDay for blackbox tests.
func SummarizeDay(events []Event) (string, string) { return summarizeDay(events) }

// RSSIToBars wraps rssiToBars for blackbox tests.
func RSSIToBars(rssi int) int { return rssiToBars(rssi) }

// RenderImage wraps renderImage for blackbox tests.
func RenderImage(d DisplayData) (image.Image, error) { return renderImage(d) }

// StatusFromQuery wraps statusFromQuery for blackbox tests.
func StatusFromQuery(r *http.Request) (int, int) { return statusFromQuery(r) }

// ChipTimeString wraps chipTimeString for blackbox tests.
func ChipTimeString(ev Event) string { return chipTimeString(ev) }

// Truncate wraps truncate for blackbox tests.
func Truncate(s string, n int) string { return truncate(s, n) }

// DaysBetween wraps daysBetween for blackbox tests.
func DaysBetween(a, b time.Time) int { return daysBetween(a, b) }

// FetchEventsIcal wraps fetchEventsIcal for blackbox tests.
func FetchEventsIcal(ctx context.Context, url string, loc *time.Location) ([]Event, error) {
	return fetchEventsIcal(ctx, url, loc)
}

// NewTestHandler returns the HTTP handler that Run installs, pre-loaded with
// the given events, without starting a listener, refresh loop, or Google
// Calendar fetch. Suitable for use with httptest.NewServer in handler tests.
func NewTestHandler(loc *time.Location, events []Event, fetchedAt time.Time) http.Handler {
	s := &server{
		cfg:      Config{},
		loc:      loc,
		mu:       sync.RWMutex{},
		cached:   events,
		cachedAt: fetchedAt,
		renderFn: nil,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/calendar.bin", s.handleBin)
	mux.HandleFunc("/calendar.png", s.handlePNG)
	mux.HandleFunc("/calendar.demo.png", s.handleDemoPNG)
	mux.HandleFunc("/healthz", s.handleHealth)
	return mux
}

// NewTestHandlerWithRenderer is like NewTestHandler but uses a custom render
// function. Used to inject a renderer that returns wrong-sized images.
func NewTestHandlerWithRenderer(
	loc *time.Location,
	events []Event,
	fetchedAt time.Time,
	renderFn func(DisplayData) (image.Image, error),
) http.Handler {
	s := &server{
		cfg:      Config{},
		loc:      loc,
		mu:       sync.RWMutex{},
		cached:   events,
		cachedAt: fetchedAt,
		renderFn: renderFn,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/calendar.bin", s.handleBin)
	mux.HandleFunc("/calendar.png", s.handlePNG)
	mux.HandleFunc("/calendar.demo.png", s.handleDemoPNG)
	mux.HandleFunc("/healthz", s.handleHealth)
	return mux
}

// Server is the internal server type exposed to blackbox tests.
type Server = server

// NewTestServer creates a server pre-loaded with no events, suitable for
// calling Refresh or SetCached in tests.
func NewTestServer(cfg Config, loc *time.Location) *Server {
	return &server{
		cfg:      cfg,
		loc:      loc,
		mu:       sync.RWMutex{},
		cached:   nil,
		cachedAt: time.Time{},
		renderFn: nil,
	}
}

// SetCached replaces the cached events without going through a real fetch.
func (s *Server) SetCached(events []Event, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cached = events
	s.cachedAt = at
}

// Refresh calls the internal refresh method, which fetches events from the
// iCal feed and updates the cache only on success.
func (s *Server) Refresh(ctx context.Context) error { return s.refresh(ctx) }

// Cached returns a copy of the currently cached events.
func (s *Server) Cached() []Event {
	return s.snapshot()
}

// ICalPropTimes wraps icalPropTimes for blackbox tests.
func ICalPropTimes(prop *ics.IANAProperty, loc *time.Location) []time.Time {
	return icalPropTimes(prop, loc)
}

// MaxRecurrenceOccurrences exposes the cap constant for assertion in tests.
const MaxRecurrenceOccurrences = maxRecurrenceOccurrences

// EventsFromICS parses an iCal string and returns events in [timeMin, timeMax],
// expanding recurring events. Provides a deterministic test entry point for
// eventsFromCal without depending on time.Now().
func EventsFromICS(body string, loc *time.Location, timeMin, timeMax time.Time) ([]Event, error) {
	cal, err := ics.ParseCalendar(strings.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("parse ical: %w", err)
	}
	return eventsFromCal(cal, loc, timeMin, timeMax), nil
}
