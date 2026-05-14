package calendar_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"calendar-display/internal/calendar"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRun_Errors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		setup      func(t *testing.T) calendar.Config
		wantSubstr string
	}{
		{
			"bad timezone",
			func(t *testing.T) calendar.Config {
				t.Helper()
				return calendar.Config{
					Timezone:        "Not/A/Timezone",
					CredentialsPath: "/nonexistent",
					TokenPath:       "/nonexistent",
					ListenAddr:      ":0",
					CalendarID:      testCalendarPrimary,
					FetchInterval:   time.Minute,
				}
			},
			"invalid timezone",
		},
		{
			"missing credentials",
			func(t *testing.T) calendar.Config {
				t.Helper()
				return calendar.Config{
					Timezone:        testTimezoneUTC,
					CredentialsPath: filepath.Join(t.TempDir(), "nonexistent.json"),
					TokenPath:       filepath.Join(t.TempDir(), "token.json"),
					ListenAddr:      ":0",
					CalendarID:      testCalendarPrimary,
					FetchInterval:   time.Minute,
				}
			},
			"read ",
		},
		{
			"malformed credentials",
			func(t *testing.T) calendar.Config {
				t.Helper()
				credsPath := filepath.Join(t.TempDir(), "creds.json")
				require.NoError(t, os.WriteFile(credsPath, []byte(`{invalid json}`), 0600))
				return calendar.Config{
					Timezone:        testTimezoneUTC,
					CredentialsPath: credsPath,
					TokenPath:       filepath.Join(t.TempDir(), "token.json"),
					ListenAddr:      ":0",
					CalendarID:      testCalendarPrimary,
					FetchInterval:   time.Minute,
				}
			},
			"parse credentials",
		},
		{
			"missing token",
			func(t *testing.T) calendar.Config {
				t.Helper()
				credsData, ioErr := os.ReadFile("testdata/credentials.json")
				require.NoError(t, ioErr)
				credsPath := filepath.Join(t.TempDir(), "creds.json")
				require.NoError(t, os.WriteFile(credsPath, credsData, 0600))
				return calendar.Config{
					Timezone:        testTimezoneUTC,
					CredentialsPath: credsPath,
					TokenPath:       filepath.Join(t.TempDir(), "nonexistent-token.json"),
					ListenAddr:      ":0",
					CalendarID:      testCalendarPrimary,
					FetchInterval:   time.Minute,
				}
			},
			"read token (run with -auth first?)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := calendar.Run(tc.setup(t))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantSubstr)
		})
	}
}
