package calendar_test

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	"calendar-display/internal/calendar"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunAuth_CredentialErrors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		setup      func(t *testing.T) calendar.Config
		wantSubstr string
	}{
		{
			"missing credentials",
			func(t *testing.T) calendar.Config {
				t.Helper()
				return calendar.Config{
					CredentialsPath: filepath.Join(t.TempDir(), "nonexistent.json"),
					AuthListenPort:  0,
				}
			},
			"read ",
		},
		{
			"malformed credentials",
			func(t *testing.T) calendar.Config {
				t.Helper()
				credsPath := filepath.Join(t.TempDir(), "creds.json")
				require.NoError(t, os.WriteFile(credsPath, []byte(`{not json}`), 0600))
				return calendar.Config{
					CredentialsPath: credsPath,
					AuthListenPort:  0,
				}
			},
			"parse credentials",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := calendar.RunAuth(tc.setup(t))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantSubstr)
		})
	}
}

func TestRunAuth_PortInUse(t *testing.T) {
	t.Parallel()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()
	addr, ok := ln.Addr().(*net.TCPAddr)
	require.True(t, ok, "listener addr is not *net.TCPAddr")
	port := addr.Port

	credsData, ioErr := os.ReadFile("testdata/credentials.json")
	require.NoError(t, ioErr)
	credsPath := filepath.Join(t.TempDir(), "creds.json")
	require.NoError(t, os.WriteFile(credsPath, credsData, 0600))

	cfg := calendar.Config{
		CredentialsPath: credsPath,
		AuthListenPort:  port,
	}
	authErr := calendar.RunAuth(cfg)
	require.Error(t, authErr)
	assert.Contains(t, authErr.Error(), "listen on")
}
