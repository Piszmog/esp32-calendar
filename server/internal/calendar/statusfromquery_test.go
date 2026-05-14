package calendar_test

import (
	"net/http/httptest"
	"testing"

	"calendar-display/internal/calendar"

	"github.com/stretchr/testify/assert"
)

func TestStatusFromQuery(t *testing.T) {
	t.Parallel()
	unknown := calendar.RSSIUnknown
	cases := []struct {
		name     string
		query    string
		wantBat  int
		wantRSSI int
	}{
		{"no params", "", -1, unknown},
		{"bat only", "bat=42", 42, unknown},
		{"rssi only", "rssi=-60", -1, -60},
		{"both", "bat=87&rssi=-55", 87, -55},
		{"bat invalid", "bat=abc", -1, unknown},
		{"rssi invalid", "rssi=foo", -1, unknown},
		{"bat zero", "bat=0", 0, unknown},
		{"rssi zero", "rssi=0", -1, 0},
		{"both invalid", "bat=x&rssi=y", -1, unknown},
		{"bat negative clamped to 0", "bat=-5", 0, unknown},
		{"bat over 100 clamped to 100", "bat=200", 100, unknown},
		{"bat exactly 100", "bat=100", 100, unknown},
		{"rssi positive clamped to 0", "rssi=999", -1, 0},
		{"rssi too low clamped to -120", "rssi=-200", -1, -120},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			url := "/calendar.bin"
			if tc.query != "" {
				url += "?" + tc.query
			}
			r := httptest.NewRequestWithContext(t.Context(), "GET", url, nil)
			bat, rssi := calendar.StatusFromQuery(r)
			assert.Equal(t, tc.wantBat, bat, "bat")
			assert.Equal(t, tc.wantRSSI, rssi, "rssi")
		})
	}
}
