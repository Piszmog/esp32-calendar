// Package calendar fetches events from an iCal feed, renders them as a
// 1-bit bitmap matching the layout for a Waveshare 7.5" e-paper, and serves
// the bitmap (plus a PNG preview) over HTTP.
package calendar

import (
	"context"
	"errors"
	"fmt"
	"image"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"
)

var (
	errNoICalURL        = errors.New("ical URL required: set ICAL_URL env var or -ical-url flag")
	errNoTimezone       = errors.New("timezone required: set -tz to an IANA name, e.g. America/New_York")
	errBadFetchInterval = errors.New("fetch interval must be positive")
)

const (
	shutdownTimeout       = 5 * time.Second
	httpReadHeaderTimeout = 5 * time.Second
	httpReadTimeout       = 10 * time.Second
	httpWriteTimeout      = 30 * time.Second
	httpIdleTimeout       = 60 * time.Second
	// staleAfterIntervals is how many fetch intervals may pass without a
	// successful fetch before the data is reported stale (healthz 503 and a
	// "(stale)" marker in the footer).
	staleAfterIntervals = 3
	demoDefaultRSSI     = -55
	demoDefaultBatPct   = 87
	maxBatPct           = 100
	// rssiUnknown is the sentinel returned by statusFromQuery when the rssi
	// query param is absent. Any positive value is impossible for real WiFi
	// RSSI (always negative dBm), so 1 is unambiguous.
	rssiUnknown = 1
	rssiMin     = -120
)

// Config holds all runtime configuration. The zero value is not useful;
// callers should populate every field (cmd/server does this from flags).
type Config struct {
	ListenAddr    string
	ICalURL       string
	Timezone      string
	FetchInterval time.Duration
}

// server is the running HTTP service. It holds the most recent batch of
// events from the iCal feed and re-renders the image on every request.
type server struct {
	cfg      Config
	loc      *time.Location
	mu       sync.RWMutex
	cached   []event
	cachedAt time.Time
	// lastErr and consecutiveFailures describe fetches since the last success.
	lastErr             error
	consecutiveFailures int
	// renderFn overrides renderImage when non-nil; used only in tests.
	renderFn func(displayData) image.Image
}

// Run starts the HTTP server and blocks until SIGINT/SIGTERM.
// It performs an initial calendar fetch synchronously so a misconfigured
// deployment (bad config, network down) fails fast.
func Run(cfg Config) error {
	loc, err := validateConfig(cfg)
	if err != nil {
		return err
	}

	// Parse the embedded fonts now so a bad embed fails at startup, not on
	// the first request.
	_, _ = loadFonts()

	s := &server{
		cfg:      cfg,
		loc:      loc,
		mu:       sync.RWMutex{},
		cached:   nil,
		cachedAt: time.Time{},
		renderFn: nil,
	}

	if err := s.refresh(context.Background()); err != nil {
		return fmt.Errorf("initial calendar fetch failed: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	var loopDone sync.WaitGroup
	loopDone.Go(func() { s.refreshLoop(ctx) })

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           s.routes(),
		ReadHeaderTimeout: httpReadHeaderTimeout,
		ReadTimeout:       httpReadTimeout,
		WriteTimeout:      httpWriteTimeout,
		IdleTimeout:       httpIdleTimeout,
	}

	serveErr := make(chan error, 1)
	go func() {
		log.Printf("listening on %s", cfg.ListenAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(stop)

	select {
	case <-stop:
		log.Println("shutting down")
	case err := <-serveErr:
		cancel()
		loopDone.Wait()
		return fmt.Errorf("listen: %w", err)
	}

	return gracefulShutdown(srv, cancel, &loopDone, serveErr)
}

// routes returns the HTTP handler for all endpoints.
func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/calendar.bin", s.handleBin)
	mux.HandleFunc("/calendar.png", s.handlePNG)
	mux.HandleFunc("/calendar.demo.png", s.handleDemoPNG)
	mux.HandleFunc("/healthz", s.handleHealth)
	return mux
}

// validateConfig checks cfg and returns the loaded timezone.
func validateConfig(cfg Config) (*time.Location, error) {
	if cfg.Timezone == "" {
		return nil, errNoTimezone
	}
	loc, err := time.LoadLocation(cfg.Timezone)
	if err != nil {
		return nil, fmt.Errorf("invalid timezone %q: %w", cfg.Timezone, err)
	}
	if cfg.FetchInterval <= 0 {
		return nil, fmt.Errorf("%w, got %s", errBadFetchInterval, cfg.FetchInterval)
	}
	if cfg.ICalURL == "" {
		return nil, errNoICalURL
	}
	return loc, nil
}

// gracefulShutdown stops the HTTP server, cancels the refresh loop, and
// surfaces any late listen error that arrived during the drain window.
func gracefulShutdown(srv *http.Server, cancel context.CancelFunc, loopDone *sync.WaitGroup, serveErr <-chan error) error {
	shutdownCtx, c := context.WithTimeout(context.Background(), shutdownTimeout)
	defer c()
	shutdownErr := srv.Shutdown(shutdownCtx)

	cancel()
	loopDone.Wait()

	// Drain a pending listen error that arrived during shutdown.
	select {
	case err := <-serveErr:
		if shutdownErr == nil {
			shutdownErr = fmt.Errorf("listen: %w", err)
		}
	default:
	}

	if shutdownErr != nil {
		return fmt.Errorf("shutdown: %w", shutdownErr)
	}
	return nil
}

func (s *server) refreshLoop(ctx context.Context) {
	t := time.NewTicker(s.cfg.FetchInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.refresh(ctx); err != nil {
				log.Printf("refresh: %v", err)
			}
		}
	}
}

func (s *server) refresh(ctx context.Context) error {
	events, err := fetchEventsIcal(ctx, s.cfg.ICalURL, s.loc)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.lastErr = err
		s.consecutiveFailures++
		return err
	}
	s.cached = events
	s.cachedAt = time.Now()
	s.lastErr = nil
	s.consecutiveFailures = 0
	log.Printf("refreshed: %d events", len(events))
	return nil
}

// snapshot returns a copy of the cached events and when they were fetched.
func (s *server) snapshot() ([]event, time.Time) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cp := make([]event, len(s.cached))
	copy(cp, s.cached)
	return cp, s.cachedAt
}

// isStale reports whether data fetched at fetchedAt is older than
// staleAfterIntervals fetch intervals.
func (s *server) isStale(fetchedAt, now time.Time) bool {
	return now.Sub(fetchedAt) > staleAfterIntervals*s.cfg.FetchInterval
}

// liveDisplayData builds the display data for the cached events, including
// fetch time and staleness for the footer.
func (s *server) liveDisplayData(bat, rssi int) displayData {
	events, fetchedAt := s.snapshot()
	now := time.Now().In(s.loc)
	data := buildDisplayData(events, s.loc, bat, rssi, now)
	data.FetchedAt = fetchedAt.In(s.loc)
	data.Stale = s.isStale(fetchedAt, now)
	return data
}

// statusFromQuery parses ?bat=NN&rssi=NN sent by the ESP32 in the calendar.bin
// request. batPct is -1 if the param is absent (renderer hides the icon).
// bat is clamped to [0, 100]. rssi is rssiUnknown if absent, else clamped to
// [rssiMin, 0] to ensure non-negative values don't map to full bars.
func statusFromQuery(r *http.Request) (int, int) {
	batPct := -1
	rssi := rssiUnknown
	if v := r.URL.Query().Get("bat"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			if n < 0 {
				n = 0
			} else if n > maxBatPct {
				n = maxBatPct
			}
			batPct = n
		}
	}
	if v := r.URL.Query().Get("rssi"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			if n > 0 {
				n = 0
			} else if n < rssiMin {
				n = rssiMin
			}
			rssi = n
		}
	}
	return batPct, rssi
}

func (s *server) doRender(d displayData) image.Image {
	if s.renderFn != nil {
		return s.renderFn(d)
	}
	return renderImage(d)
}

func (s *server) handleBin(w http.ResponseWriter, r *http.Request) {
	bat, rssi := statusFromQuery(r)
	img := s.doRender(s.liveDisplayData(bat, rssi))
	if b := img.Bounds(); b.Dx() != imgW || b.Dy() != imgH {
		log.Printf("pack: image %dx%d ≠ expected %dx%d", b.Dx(), b.Dy(), imgW, imgH)
		http.Error(w, fmt.Sprintf("unexpected image size %dx%d", b.Dx(), b.Dy()), http.StatusInternalServerError)
		return
	}
	packed := pack1Bit(img)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(len(packed)))
	if _, err := w.Write(packed); err != nil {
		log.Printf("bin write: %v", err)
	}
}

func (s *server) handlePNG(w http.ResponseWriter, r *http.Request) {
	bat, rssi := statusFromQuery(r)
	if bat < 0 {
		bat = demoDefaultBatPct // demo defaults so the preview always looks complete
	}
	if rssi == rssiUnknown {
		rssi = demoDefaultRSSI
	}
	img := s.doRender(s.liveDisplayData(bat, rssi))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "image/png")
	if err := writePNG(w, img); err != nil {
		log.Printf("png write: %v", err)
	}
}

func (s *server) handleDemoPNG(w http.ResponseWriter, r *http.Request) {
	events, now := demoEvents(s.loc)
	img := s.doRender(buildDisplayData(events, s.loc, demoDefaultBatPct, demoDefaultRSSI, now))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "image/png")
	if err := writePNG(w, img); err != nil {
		log.Printf("demo png write: %v", err)
	}
}

// demoEvents returns a fixed, hardcoded event list and reference time for use
// in the demo endpoint. The fixed date keeps the rendered PNG reproducible
// across server restarts so the README screenshot stays stable.
func demoEvents(loc *time.Location) ([]event, time.Time) {
	now := time.Date(2026, 5, 13, 14, 30, 0, 0, loc)
	return []event{
		// Today — one long title to exercise chip truncation, one short
		{Start: time.Date(2026, 5, 13, 15, 0, 0, 0, loc), End: time.Time{}, Title: "Architecture review with the platform team", AllDay: false},
		{Start: time.Date(2026, 5, 13, 17, 30, 0, 0, loc), End: time.Time{}, Title: "Gym", AllDay: false},
		// Tomorrow — one all-day + one timed
		{Start: time.Date(2026, 5, 14, 0, 0, 0, 0, loc), End: time.Date(2026, 5, 15, 0, 0, 0, 0, loc), Title: "Conference Day 1", AllDay: true},
		{Start: time.Date(2026, 5, 14, 11, 0, 0, 0, loc), End: time.Time{}, Title: "Lunch with Alex", AllDay: false},
		// Week Ahead — Fri with 2 events (exercises multi-event summary), Mon, Tue all-day
		{Start: time.Date(2026, 5, 15, 9, 0, 0, 0, loc), End: time.Time{}, Title: "1:1 Jamie", AllDay: false},
		{Start: time.Date(2026, 5, 15, 14, 0, 0, 0, loc), End: time.Time{}, Title: "Design crit", AllDay: false},
		{Start: time.Date(2026, 5, 18, 10, 0, 0, 0, loc), End: time.Time{}, Title: "Sprint planning", AllDay: false},
		{Start: time.Date(2026, 5, 19, 0, 0, 0, 0, loc), End: time.Date(2026, 5, 20, 0, 0, 0, 0, loc), Title: "Holiday", AllDay: true},
	}, now
}

// handleHealth reports "ok" (200) while the cached events are fresh, and
// "stale" (503) once no fetch has succeeded for staleAfterIntervals fetch
// intervals, so monitoring notices a broken feed.
func (s *server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	s.mu.RLock()
	fetchedAt := s.cachedAt
	n := len(s.cached)
	failures := s.consecutiveFailures
	lastErr := ""
	if s.lastErr != nil {
		lastErr = s.lastErr.Error()
	}
	s.mu.RUnlock()

	now := time.Now()
	status, code := "ok", http.StatusOK
	if s.isStale(fetchedAt, now) {
		status, code = "stale", http.StatusServiceUnavailable
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(code)
	_, _ = fmt.Fprintf(w, "%s\nlast_fetch_age=%s\nevents=%d\nconsecutive_failures=%d\nlast_error=%s\n",
		status, now.Sub(fetchedAt), n, failures, lastErr)
}
