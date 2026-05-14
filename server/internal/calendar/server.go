// Package calendar fetches events from Google Calendar, renders them as a
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

	"google.golang.org/api/option"
)

const (
	shutdownTimeout   = 5 * time.Second
	httpReadTimeout   = 10 * time.Second
	httpWriteTimeout  = 30 * time.Second
	demoDefaultRSSI   = -55
	demoDefaultBatPct = 87
	maxBatPct         = 100
	// rssiUnknown is the sentinel returned by statusFromQuery when the rssi
	// query param is absent. Any positive value is impossible for real WiFi
	// RSSI (always negative dBm), so 1 is unambiguous.
	rssiUnknown = 1
	rssiMin     = -120
)

// Config holds all runtime configuration. The zero value is not useful;
// callers should populate every field (cmd/server does this from flags).
type Config struct {
	ListenAddr      string
	CredentialsPath string
	TokenPath       string
	CalendarID      string
	Timezone        string
	FetchInterval   time.Duration
	AuthMode        bool
	AuthListenPort  int
	// clientOpts are injected in tests to redirect the Calendar API to a
	// local httptest.Server. Nil in production.
	clientOpts []option.ClientOption
}

// server is the running HTTP service. It holds the most recent batch of
// events from Google Calendar and re-renders the image on every request.
type server struct {
	cfg      Config
	loc      *time.Location
	mu       sync.RWMutex
	cached   []event
	cachedAt time.Time
	// renderFn overrides renderImage when non-nil; used only in tests.
	renderFn func(displayData) (image.Image, error)
}

// Run starts the HTTP server and blocks until SIGINT/SIGTERM.
// It performs an initial calendar fetch synchronously so a misconfigured
// deployment (missing creds, bad timezone, network down) fails fast.
func Run(cfg Config) error {
	loc, err := time.LoadLocation(cfg.Timezone)
	if err != nil {
		return fmt.Errorf("invalid timezone %q: %w", cfg.Timezone, err)
	}

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

	mux := http.NewServeMux()
	mux.HandleFunc("/calendar.bin", s.handleBin)
	mux.HandleFunc("/calendar.png", s.handlePNG)
	mux.HandleFunc("/calendar.demo.png", s.handleDemoPNG)
	mux.HandleFunc("/healthz", s.handleHealth)

	srv := &http.Server{
		Addr:         cfg.ListenAddr,
		Handler:      mux,
		ReadTimeout:  httpReadTimeout,
		WriteTimeout: httpWriteTimeout,
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
	events, err := fetchEvents(ctx, s.cfg, s.loc)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.cached = events
	s.cachedAt = time.Now()
	s.mu.Unlock()
	log.Printf("refreshed: %d events", len(events))
	return nil
}

func (s *server) snapshot() []event {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cp := make([]event, len(s.cached))
	copy(cp, s.cached)
	return cp
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

func (s *server) doRender(d displayData) (image.Image, error) {
	if s.renderFn != nil {
		return s.renderFn(d)
	}
	return renderImage(d)
}

func (s *server) handleBin(w http.ResponseWriter, r *http.Request) {
	bat, rssi := statusFromQuery(r)
	events := s.snapshot()
	data := buildDisplayData(events, s.loc, bat, rssi, time.Now().In(s.loc))
	img, err := s.doRender(data)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
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
	events := s.snapshot()
	data := buildDisplayData(events, s.loc, bat, rssi, time.Now().In(s.loc))
	img, err := s.doRender(data)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "image/png")
	if err := writePNG(w, img); err != nil {
		log.Printf("png write: %v", err)
	}
}

func (s *server) handleDemoPNG(w http.ResponseWriter, r *http.Request) {
	events, now := demoEvents(s.loc)
	data := buildDisplayData(events, s.loc, demoDefaultBatPct, demoDefaultRSSI, now)
	img, err := s.doRender(data)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
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

func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	age := time.Since(s.cachedAt)
	n := len(s.cached)
	s.mu.RUnlock()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = fmt.Fprintf(w, "ok\nlast_fetch_age=%s\nevents=%d\n", age, n)
}
