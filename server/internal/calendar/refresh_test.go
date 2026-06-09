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

// TestRefresh_PreservesCacheOnFailure verifies that a failed iCal fetch leaves
// the cached events unchanged. This matters because the ESP32 polls every
// 30 min — a transient server outage must not blank the screen.
func TestRefresh_PreservesCacheOnFailure(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "backend error", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	cfg := calendar.Config{
		ICalURL: srv.URL,
	}
	s := calendar.NewTestServer(cfg, time.UTC)

	originalEvents := []calendar.Event{
		{Start: time.Date(2026, 5, 11, 10, 0, 0, 0, time.UTC), Title: "Existing Event 1"},
		{Start: time.Date(2026, 5, 11, 14, 0, 0, 0, time.UTC), Title: "Existing Event 2"},
	}
	s.SetCached(originalEvents, time.Date(2026, 5, 11, 9, 0, 0, 0, time.UTC))

	refreshErr := s.Refresh(context.Background())
	require.Error(t, refreshErr, "Refresh should return error on 500 response")

	cached := s.Cached()
	require.Len(t, cached, 2, "cache must be unchanged after failed refresh")
	assert.Equal(t, "Existing Event 1", cached[0].Title)
	assert.Equal(t, "Existing Event 2", cached[1].Title)
}
