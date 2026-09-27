// Command calendar-server renders iCal feed events as a 1-bit bitmap
// served over HTTP for an ESP32 e-paper client.
package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"time"
	_ "time/tzdata" // embedded zoneinfo: -tz and iCal TZIDs resolve without /usr/share/zoneinfo

	"calendar-display/internal/calendar"
)

// version is set at build time via -ldflags "-X main.version=..."
// by goreleaser. Defaults to "dev" for local builds.
var version = "dev"

const (
	defaultFetchInterval = 10 * time.Minute
	// exitUsage matches the status flag.ExitOnError uses for a bad flag.
	exitUsage = 2
)

// parseConfig resolves flags from args, falling back to getenv for the
// secrets (ICAL_URL, AUTH_TOKEN) when their flags are empty.
func parseConfig(args []string, getenv func(string) string) (calendar.Config, bool, error) {
	var cfg calendar.Config
	var showVersion bool

	fs := flag.NewFlagSet("calendar-server", flag.ContinueOnError)
	fs.BoolVar(&showVersion, "version", false, "Print version and exit")
	fs.StringVar(&cfg.ListenAddr, "listen", ":8080", "HTTP listen address (use 127.0.0.1:PORT to restrict to loopback)")
	fs.StringVar(&cfg.ICalURL, "ical-url", "", "Secret iCal URL from Google Calendar settings")
	fs.StringVar(&cfg.Timezone, "tz", "America/Los_Angeles", "IANA timezone, e.g. America/New_York")
	fs.StringVar(&cfg.AuthToken, "auth-token", "", "Shared secret required on /calendar.* endpoints (prefer AUTH_TOKEN env var)")
	fs.DurationVar(&cfg.FetchInterval, "fetch-interval", defaultFetchInterval, "How often to poll the iCal feed")
	if err := fs.Parse(args); err != nil {
		return cfg, false, fmt.Errorf("parse flags: %w", err)
	}

	if cfg.ICalURL == "" {
		cfg.ICalURL = getenv("ICAL_URL")
	}
	if cfg.AuthToken == "" {
		cfg.AuthToken = getenv("AUTH_TOKEN")
	}
	return cfg, showVersion, nil
}

func main() {
	cfg, showVersion, err := parseConfig(os.Args[1:], os.Getenv)
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		os.Exit(exitUsage) // the FlagSet already printed the error and usage
	}

	if showVersion {
		_, _ = fmt.Fprintln(os.Stdout, version)
		return
	}

	log.Printf("calendar-server %s starting", version)

	if err := calendar.Run(cfg); err != nil {
		log.Fatalf("run: %v", err)
	}
}
