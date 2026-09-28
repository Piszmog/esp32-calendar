package calendar_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
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

// TestRefreshLoop_FetchesUntilCancelled verifies the loop refetches on every
// tick and returns promptly once its context is cancelled.
func TestRefreshLoop_FetchesUntilCancelled(t *testing.T) {
	t.Parallel()

	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(icsFixture))
	}))
	t.Cleanup(srv.Close)

	s := calendar.NewTestServer(calendar.Config{ICalURL: srv.URL, FetchInterval: 10 * time.Millisecond}, time.UTC)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		s.RefreshLoop(ctx)
		close(done)
	}()

	require.Eventually(t, func() bool { return hits.Load() >= 2 }, 5*time.Second, 5*time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("refreshLoop did not return after cancel")
	}
}

func TestGracefulShutdown(t *testing.T) {
	t.Parallel()

	errLate := errors.New("address in use")
	cases := []struct {
		name    string
		lateErr error
		wantErr error
	}{
		{"clean", nil, nil},
		{"late listen error surfaces", errLate, errLate},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			serveErr := make(chan error, 1)
			if tc.lateErr != nil {
				serveErr <- tc.lateErr
			}
			ctx, cancel := context.WithCancel(t.Context())
			var loopDone sync.WaitGroup
			loopDone.Go(func() { <-ctx.Done() })

			err := calendar.GracefulShutdown(&http.Server{ReadHeaderTimeout: time.Second}, cancel, &loopDone, serveErr)
			if tc.wantErr == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, tc.wantErr)
			}
			assert.Error(t, ctx.Err(), "shutdown must cancel the refresh loop")
		})
	}
}
