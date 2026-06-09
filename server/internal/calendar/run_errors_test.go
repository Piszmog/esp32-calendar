package calendar_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"calendar-display/internal/calendar"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRun_Errors(t *testing.T) {
	t.Parallel()

	badICalSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "server error", http.StatusInternalServerError)
	}))
	t.Cleanup(badICalSrv.Close)

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
			"ical server error",
			calendar.Config{
				Timezone:      testTimezoneUTC,
				ListenAddr:    ":0",
				FetchInterval: time.Minute,
				ICalURL:       badICalSrv.URL,
			},
			"fetch ical",
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
