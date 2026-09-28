package calendar_test

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"calendar-display/internal/calendar"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// unusedICalURL is never fetched: validation fails before the first fetch.
const unusedICalURL = "http://unused.invalid"

func TestRun_Errors(t *testing.T) {
	t.Parallel()

	badICalSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "server error", http.StatusInternalServerError)
	}))
	t.Cleanup(badICalSrv.Close)

	goodICalSrv := icalServer(t, icsFixture)
	t.Cleanup(goodICalSrv.Close)
	// Held open so Run's ListenAndServe fails after the initial fetch succeeds.
	busy, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = busy.Close() })

	cases := []struct {
		name       string
		cfg        calendar.Config
		wantSubstr string
	}{
		{
			"bad timezone",
			calendar.Config{
				Timezone:      "Not/A/Timezone",
				ListenAddr:    ":0",
				FetchInterval: time.Minute,
			},
			"invalid timezone",
		},
		{
			"empty timezone",
			calendar.Config{
				Timezone:      "",
				ListenAddr:    ":0",
				FetchInterval: time.Minute,
				ICalURL:       unusedICalURL,
			},
			"timezone required",
		},
		{
			"zero fetch interval",
			calendar.Config{
				Timezone:      testTimezoneUTC,
				ListenAddr:    ":0",
				FetchInterval: 0,
				ICalURL:       unusedICalURL,
			},
			"fetch interval must be positive",
		},
		{
			"negative fetch interval",
			calendar.Config{
				Timezone:      testTimezoneUTC,
				ListenAddr:    ":0",
				FetchInterval: -time.Minute,
				ICalURL:       unusedICalURL,
			},
			"fetch interval must be positive",
		},
		{
			"missing ical url",
			calendar.Config{
				Timezone:      testTimezoneUTC,
				ListenAddr:    ":0",
				FetchInterval: time.Minute,
				ICalURL:       "",
			},
			"ical URL required",
		},
		{
			"ical server error",
			calendar.Config{
				Timezone:      testTimezoneUTC,
				ListenAddr:    ":0",
				FetchInterval: time.Minute,
				ICalURL:       badICalSrv.URL,
			},
			"fetch ical",
		},
		{
			"listen address in use",
			calendar.Config{
				Timezone:      testTimezoneUTC,
				ListenAddr:    busy.Addr().String(),
				FetchInterval: time.Minute,
				ICalURL:       goodICalSrv.URL,
			},
			"listen:",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := calendar.Run(tc.cfg)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantSubstr)
		})
	}
}
