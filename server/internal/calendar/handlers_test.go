package calendar_test

import (
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
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
	ok, _ := regexp.Match(`^ok\nlast_fetch_age=.+\nevents=\d+\n$`, body)
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
	wrongRenderer := func(_ calendar.DisplayData) (image.Image, error) {
		return image.NewRGBA(image.Rect(0, 0, 100, 100)), nil
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
