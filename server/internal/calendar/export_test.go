package calendar

import (
	"context"
	"image"
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"google.golang.org/api/option"
)

// StartOAuthCallback wraps startOAuthCallback for blackbox tests.
func StartOAuthCallback(ln net.Listener, state string) (<-chan string, <-chan error) {
	return startOAuthCallback(ln, state)
}

// ErrStateMismatch exposes errStateMismatch for assertion in blackbox tests.
var ErrStateMismatch = errStateMismatch

// ErrNoCode exposes errNoCode for assertion in blackbox tests.
var ErrNoCode = errNoCode

// ErrOAuthFailed exposes errOAuthFailed for assertion in blackbox tests.
var ErrOAuthFailed = errOAuthFailed

// RSSIUnknown exposes rssiUnknown so tests can assert the absent-param sentinel.
const RSSIUnknown = rssiUnknown

// LoadToken wraps loadToken for blackbox tests.
func LoadToken(path string) (*oauth2.Token, error) { return loadToken(path) }

// DaysBetween wraps daysBetween for blackbox tests.
func DaysBetween(a, b time.Time) int { return daysBetween(a, b) }

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

// FetchEvents wraps fetchEvents for blackbox tests.
func FetchEvents(ctx context.Context, c Config, loc *time.Location) ([]Event, error) {
	return fetchEvents(ctx, c, loc)
}

// SetTestClientOpts injects API client options into c so that fetchEvents
// redirects calls to a local httptest.Server. Because opts are per-Config,
// parallel tests do not share state and no cleanup defer is needed.
func SetTestClientOpts(c *Config, opts []option.ClientOption) {
	c.clientOpts = opts
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
// Calendar API and updates the cache only on success.
func (s *Server) Refresh(ctx context.Context) error { return s.refresh(ctx) }

// Cached returns a copy of the currently cached events.
func (s *Server) Cached() []Event {
	return s.snapshot()
}
