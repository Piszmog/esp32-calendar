package calendar_test

import (
	"image"
	"image/png"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"calendar-display/internal/calendar"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testHandler returns a handler with two pre-loaded events at a fixed time
// so tests have a stable rendering baseline.
func testHandler(t *testing.T) http.Handler {
	t.Helper()
	loc := time.UTC
	now := time.Now()
	events := []calendar.Event{
		{Start: now.Add(2 * time.Hour), Title: "Future Event"},
		{Start: time.Date(now.Year(), now.Month(), now.Day()+1, 10, 0, 0, 0, loc), Title: testTitleTomorrow},
	}
	return calendar.NewTestHandler(loc, events, now)
}

func TestHandler_Healthz(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(testHandler(t))
	defer ts.Close()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+"/healthz", nil)
	require.NoError(t, err)
	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	ok, _ := regexp.Match(`^ok\nlast_fetch_age=.+\nevents=\d+\nconsecutive_failures=0\nlast_error=\n$`, body)
	assert.True(t, ok, "healthz body should match expected format, got: %s", string(body))
}

func TestHandler_CalendarBin_Shape(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(testHandler(t))
	defer ts.Close()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+"/calendar.bin", nil)
	require.NoError(t, err)
	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "application/octet-stream", resp.Header.Get("Content-Type"))
	assert.Equal(t, "48000", resp.Header.Get("Content-Length"))

	body, _ := io.ReadAll(resp.Body)
	assert.Len(t, body, 48000)
}

func TestHandler_CalendarBin_ParamsPropagate(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(testHandler(t))
	defer ts.Close()

	get := func(query string) []byte {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+"/calendar.bin"+query, nil)
		require.NoError(t, err)
		resp, err := ts.Client().Do(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return b
	}

	noBat := get("?rssi=-60")
	withBat := get("?bat=42&rssi=-60")
	// Rendering with a battery percentage shown changes the footer pixels.
	assert.NotEqual(t, noBat, withBat, "bat param should change the rendered image")
}

func TestHandler_CalendarPNG_ValidImage(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(testHandler(t))
	defer ts.Close()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+"/calendar.png", nil)
	require.NoError(t, err)
	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "image/png", resp.Header.Get("Content-Type"))

	img, err := png.Decode(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, 800, img.Bounds().Dx())
	assert.Equal(t, 480, img.Bounds().Dy())
}

func TestHandler_CalendarPNG_DemoDefaults(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(testHandler(t))
	defer ts.Close()

	readBytes := func(url string) []byte {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
		require.NoError(t, err)
		resp, err := ts.Client().Do(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return b
	}

	// No params → demo defaults (bat=87, rssi=-55).
	// bat=0&rssi=0 → explicitly zero.
	noParams := readBytes(ts.URL + "/calendar.png")
	zeros := readBytes(ts.URL + "/calendar.png?bat=0&rssi=0")
	assert.NotEqual(t, noParams, zeros, "demo defaults should render differently from bat=0&rssi=0")
}

func TestHandler_CalendarBin_SizeMismatch(t *testing.T) {
	t.Parallel()
	loc := time.UTC
	now := time.Now()
	wrongRenderer := func(_ calendar.DisplayData) image.Image {
		return image.NewRGBA(image.Rect(0, 0, 100, 100))
	}
	handler := calendar.NewTestHandlerWithRenderer(loc, nil, now, wrongRenderer)
	ts := httptest.NewServer(handler)
	defer ts.Close()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+"/calendar.bin", nil)
	require.NoError(t, err)
	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), "unexpected image size")
}

func TestHandler_DemoPNG_ValidImage(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(testHandler(t))
	defer ts.Close()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+"/calendar.demo.png", nil)
	require.NoError(t, err)
	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "image/png", resp.Header.Get("Content-Type"))

	img, err := png.Decode(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, 800, img.Bounds().Dx())
	assert.Equal(t, 480, img.Bounds().Dy())
}

func TestHandler_DemoPNG_Deterministic(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(testHandler(t))
	defer ts.Close()

	get := func() []byte {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+"/calendar.demo.png", nil)
		require.NoError(t, err)
		resp, err := ts.Client().Do(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return b
	}

	first := get()
	second := get()
	assert.Equal(t, first, second, "/calendar.demo.png should return identical bytes on repeated calls")
}

func TestHandler_CalendarBin_InvalidBat(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(testHandler(t))
	defer ts.Close()

	// ?bat=abc should fall back to -1 (hide battery icon) and still return 200.
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+"/calendar.bin?bat=abc", nil)
	require.NoError(t, err)
	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	assert.Len(t, body, 48000)
}

func TestHandler_CalendarPNG_DefaultsMatchExplicit(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(testHandler(t))
	defer ts.Close()

	get := func(query string) []byte {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+"/calendar.png"+query, nil)
		require.NoError(t, err)
		resp, err := ts.Client().Do(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return b
	}

	// No params should render identically to the explicit demo defaults.
	// Both calls happen within the same second so the "Updated HH:MM" footer
	// is identical, making the PNG bytes deterministically equal.
	noParams := get("")
	explicit := get("?bat=87&rssi=-55")
	assert.Equal(t, noParams, explicit, "/calendar.png with no params should equal ?bat=87&rssi=-55")
}

func getHealthz(t *testing.T, h http.Handler) (int, string) {
	t.Helper()
	ts := httptest.NewServer(h)
	defer ts.Close()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+"/healthz", nil)
	require.NoError(t, err)
	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func TestHandler_Healthz_Stale(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		age      time.Duration
		wantCode int
		wantHead string
	}{
		{"within threshold", 3*calendar.TestFetchInterval - time.Minute, http.StatusOK, "ok\n"},
		{"past threshold", 3*calendar.TestFetchInterval + time.Minute, http.StatusServiceUnavailable, "stale\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := calendar.NewTestHandler(time.UTC, nil, time.Now().Add(-tc.age))
			code, body := getHealthz(t, h)
			assert.Equal(t, tc.wantCode, code)
			assert.True(t, strings.HasPrefix(body, tc.wantHead), "body: %s", body)
		})
	}
}

func TestHandler_Healthz_ReportsFailures(t *testing.T) {
	t.Parallel()

	var failing atomic.Bool
	failing.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if failing.Load() {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(icsFixture))
	}))
	t.Cleanup(srv.Close)

	cfg := calendar.Config{ICalURL: srv.URL, FetchInterval: calendar.TestFetchInterval}
	s := calendar.NewTestServer(cfg, time.UTC)
	s.SetCached(nil, time.Now())

	require.Error(t, s.Refresh(t.Context()))
	require.Error(t, s.Refresh(t.Context()))

	code, body := getHealthz(t, s.Handler())
	// Two failures inside the threshold: still serving, but visible.
	assert.Equal(t, http.StatusOK, code)
	assert.Contains(t, body, "consecutive_failures=2\n")
	assert.Contains(t, body, "last_error=fetch ical: unexpected HTTP status: 401\n")

	// A successful fetch clears the failure state.
	failing.Store(false)
	require.NoError(t, s.Refresh(t.Context()))
	code, body = getHealthz(t, s.Handler())
	assert.Equal(t, http.StatusOK, code)
	assert.Contains(t, body, "consecutive_failures=0\nlast_error=\n")
}

// doGet issues a GET against h and returns the status code and headers.
func doGet(t *testing.T, h http.Handler, path string, header http.Header) (int, http.Header) {
	t.Helper()
	ts := httptest.NewServer(h)
	defer ts.Close()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+path, nil)
	require.NoError(t, err)
	maps.Copy(req.Header, header)
	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode, resp.Header
}

func TestHandler_AuthToken(t *testing.T) {
	t.Parallel()

	const token = "s3cret"
	bearer := http.Header{"Authorization": {"Bearer " + token}}
	wrong := http.Header{"Authorization": {"Bearer nope"}}

	cases := []struct {
		name     string
		token    string
		path     string
		header   http.Header
		wantCode int
	}{
		{"no token configured", "", testPathBin, nil, http.StatusOK},
		{"missing token", token, testPathBin, nil, http.StatusUnauthorized},
		{"wrong header token", token, testPathBin, wrong, http.StatusUnauthorized},
		{"wrong query token", token, "/calendar.png?token=nope", nil, http.StatusUnauthorized},
		{"header token", token, testPathBin, bearer, http.StatusOK},
		{"query token", token, "/calendar.png?token=" + token, nil, http.StatusOK},
		{"demo needs token", token, "/calendar.demo.png", nil, http.StatusUnauthorized},
		{"healthz stays open", token, "/healthz", nil, http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := calendar.Config{FetchInterval: calendar.TestFetchInterval, AuthToken: tc.token}
			s := calendar.NewTestServer(cfg, time.UTC)
			s.SetCached(nil, time.Now())
			code, _ := doGet(t, s.Handler(), tc.path, tc.header)
			assert.Equal(t, tc.wantCode, code)
		})
	}
}

func TestHandler_CalendarBin_SleepHeader(t *testing.T) {
	t.Parallel()
	code, header := doGet(t, testHandler(t), testPathBin, nil)
	require.Equal(t, http.StatusOK, code)
	n, err := strconv.Atoi(header.Get("X-Sleep-Seconds"))
	require.NoError(t, err, "X-Sleep-Seconds must be an integer")
	assert.GreaterOrEqual(t, n, 180)
	assert.LessOrEqual(t, n, 30*60+180)
}

func TestSleepSeconds(t *testing.T) {
	t.Parallel()
	kathmandu, err := time.LoadLocation("Asia/Kathmandu") // UTC+5:45
	require.NoError(t, err)

	cases := []struct {
		name string
		now  time.Time
		want int
	}{
		{"just past mark", time.Date(2026, 5, 11, 10, 0, 5, 0, time.UTC), 30*60 - 5},
		{"mid interval", time.Date(2026, 5, 11, 10, 20, 0, 0, time.UTC), 10 * 60},
		{"within guard pushes to next mark", time.Date(2026, 5, 11, 10, 28, 0, 0, time.UTC), 32 * 60},
		{"at guard boundary", time.Date(2026, 5, 11, 10, 27, 0, 0, time.UTC), 3 * 60},
		{"quarter-hour offset zone aligns locally", time.Date(2026, 5, 11, 10, 20, 0, 0, kathmandu), 10 * 60},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, calendar.SleepSeconds(tc.now))
		})
	}
}
