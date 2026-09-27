package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	flagURL   = "https://flag.example/cal.ics"
	flagToken = "flag-token"
)

func TestParseConfig(t *testing.T) {
	t.Parallel()

	env := func(vars map[string]string) func(string) string {
		return func(k string) string { return vars[k] }
	}
	secrets := map[string]string{"ICAL_URL": "https://env.example/cal.ics", "AUTH_TOKEN": "env-token"}

	cases := []struct {
		name        string
		args        []string
		env         map[string]string
		wantURL     string
		wantToken   string
		wantVersion bool
	}{
		{"flags only", []string{"-ical-url", flagURL, "-auth-token", flagToken}, nil, flagURL, flagToken, false},
		{"env only", nil, secrets, "https://env.example/cal.ics", "env-token", false},
		{"flag overrides env", []string{"-ical-url", flagURL, "-auth-token", flagToken}, secrets, flagURL, flagToken, false},
		{"neither set", nil, nil, "", "", false},
		{"version", []string{"-version"}, nil, "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg, showVersion, err := parseConfig(tc.args, env(tc.env))
			require.NoError(t, err)
			assert.Equal(t, tc.wantURL, cfg.ICalURL)
			assert.Equal(t, tc.wantToken, cfg.AuthToken)
			assert.Equal(t, tc.wantVersion, showVersion)
		})
	}
}

func TestParseConfig_Defaults(t *testing.T) {
	t.Parallel()
	cfg, _, err := parseConfig(nil, func(string) string { return "" })
	require.NoError(t, err)
	assert.Equal(t, ":8080", cfg.ListenAddr)
	assert.Equal(t, "America/Los_Angeles", cfg.Timezone)
	assert.Equal(t, 10*time.Minute, cfg.FetchInterval)
}

func TestParseConfig_BadFlag(t *testing.T) {
	t.Parallel()
	_, _, err := parseConfig([]string{"-fetch-interval", "soon"}, func(string) string { return "" })
	require.Error(t, err)
}
