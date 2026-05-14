package calendar_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"calendar-display/internal/calendar"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/option"
)

// TestRefresh_PreservesCacheOnFailure verifies that a failed calendar fetch
// leaves the cached events unchanged. This is important because the ESP32
// polls every 30 min — a transient API outage must not blank the screen.
func TestRefresh_PreservesCacheOnFailure(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"code":500,"message":"backend error"}}`, http.StatusInternalServerError)
	}))
	defer ts.Close()

	dir := t.TempDir()
	credsData, err := os.ReadFile("testdata/credentials.json")
	require.NoError(t, err)
	credsPath := filepath.Join(dir, "creds.json")
	require.NoError(t, os.WriteFile(credsPath, credsData, 0600))

	tokenPath := filepath.Join(dir, "token.json")
	tok := struct {
		AccessToken  string    `json:"access_token"`
		TokenType    string    `json:"token_type"`
		RefreshToken string    `json:"refresh_token"`
		Expiry       time.Time `json:"expiry"`
	}{
		AccessToken:  testAccessToken,
		TokenType:    testTokenType,
		RefreshToken: testRefreshToken,
		Expiry:       time.Now().Add(24 * time.Hour),
	}
	tokenBytes, marshalErr := json.Marshal(tok)
	require.NoError(t, marshalErr)
	require.NoError(t, os.WriteFile(tokenPath, tokenBytes, 0600))

	cfg := calendar.Config{
		CalendarID:      testCalendarEmail,
		CredentialsPath: credsPath,
		TokenPath:       tokenPath,
	}
	calendar.SetTestClientOpts(&cfg, []option.ClientOption{
		option.WithEndpoint(ts.URL + "/"),
	})
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
