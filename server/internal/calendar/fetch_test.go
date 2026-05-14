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

func TestFetchEvents_ParsesResponse(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("testdata/events_response.json")
	require.NoError(t, err)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(data)
	}))
	defer ts.Close()

	// Write test credentials and a non-expired token into a temp dir.
	dir := t.TempDir()
	credsData, credsErr := os.ReadFile("testdata/credentials.json")
	require.NoError(t, credsErr)
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

	events, fetchErr := calendar.FetchEvents(context.Background(), cfg, time.UTC)
	require.NoError(t, fetchErr)

	// 8 items - 1 bad dateTime - 1 no-start = 6 parsed.
	assert.Len(t, events, 6)

	// Find the all-day event and verify its flag.
	var allDayFound bool
	for _, ev := range events {
		if ev.Title == "All Day Event" {
			allDayFound = true
			assert.True(t, ev.AllDay, "All Day Event should have AllDay=true")
		}
	}
	assert.True(t, allDayFound, "should have parsed the all-day event")

	// Empty summary becomes "(no title)".
	var noTitleFound bool
	for _, ev := range events {
		if ev.Title == "(no title)" {
			noTitleFound = true
		}
	}
	assert.True(t, noTitleFound, "empty summary should become (no title)")

	// Bad dateTime item must not appear.
	for _, ev := range events {
		assert.NotEqual(t, "Bad DateTime", ev.Title)
	}

	// No-start-fields item must not appear.
	for _, ev := range events {
		assert.NotEqual(t, "No Start Fields", ev.Title)
	}
}

func TestFetchEvents_HTTPError(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"code":500,"message":"backend error"}}`, http.StatusInternalServerError)
	}))
	defer ts.Close()

	credsPath, tokenPath := writeFakeCredentials(t)
	cfg := calendar.Config{
		CalendarID:      testCalendarEmail,
		CredentialsPath: credsPath,
		TokenPath:       tokenPath,
	}
	calendar.SetTestClientOpts(&cfg, []option.ClientOption{
		option.WithEndpoint(ts.URL + "/"),
	})

	_, err := calendar.FetchEvents(context.Background(), cfg, time.UTC)
	require.Error(t, err, "500 from Calendar API should return an error")
}

func TestFetchEvents_EmptyItems(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"kind":"calendar#events","summary":"test","updated":"2026-01-01T00:00:00Z","timeZone":"UTC","accessRole":"owner","defaultReminders":[],"nextSyncToken":"test","items":[]}`))
	}))
	defer ts.Close()

	credsPath, tokenPath := writeFakeCredentials(t)
	cfg := calendar.Config{
		CalendarID:      testCalendarEmail,
		CredentialsPath: credsPath,
		TokenPath:       tokenPath,
	}
	calendar.SetTestClientOpts(&cfg, []option.ClientOption{
		option.WithEndpoint(ts.URL + "/"),
	})

	events, err := calendar.FetchEvents(context.Background(), cfg, time.UTC)
	require.NoError(t, err)
	assert.Empty(t, events)
}

// writeFakeCredentials writes testdata credentials and a non-expired token to
// a temp dir and returns their paths.
func writeFakeCredentials(t *testing.T) (string, string) {
	t.Helper()
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
	return credsPath, tokenPath
}
