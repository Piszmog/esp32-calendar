// Command calendar-server renders Google Calendar events as a 1-bit bitmap
// served over HTTP for an ESP32 e-paper client.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"calendar-display/internal/calendar"
)

// version is set at build time via -ldflags "-X main.version=..."
// by goreleaser. Defaults to "dev" for local builds.
var version = "dev"

const (
	defaultFetchInterval  = 10 * time.Minute
	defaultAuthListenPort = 8090
)

func main() {
	var cfg calendar.Config
	var showVersion bool

	flag.BoolVar(&showVersion, "version", false, "Print version and exit")
	flag.StringVar(&cfg.ListenAddr, "listen", ":8080", "HTTP listen address (use 127.0.0.1:PORT to restrict to loopback)")
	flag.StringVar(&cfg.CredentialsPath, "creds", "credentials.json", "OAuth client secrets file")
	flag.StringVar(&cfg.TokenPath, "token", "token.json", "OAuth token storage file")
	flag.StringVar(&cfg.CalendarID, "calendar", "primary", "Google Calendar ID")
	flag.StringVar(&cfg.Timezone, "tz", "America/Los_Angeles", "IANA timezone, e.g. America/New_York")
	flag.DurationVar(&cfg.FetchInterval, "fetch-interval", defaultFetchInterval, "How often to poll Google Calendar")
	flag.BoolVar(&cfg.AuthMode, "auth", false, "Run OAuth flow and exit (first-time setup)")
	flag.IntVar(&cfg.AuthListenPort, "auth-port", defaultAuthListenPort, "Local port for OAuth callback (when -auth)")
	flag.Parse()

	if showVersion {
		_, _ = fmt.Fprintln(os.Stdout, version)
		return
	}

	log.Printf("calendar-server %s starting", version)

	if cfg.AuthMode {
		if err := calendar.RunAuth(cfg); err != nil {
			log.Fatalf("auth failed: %v", err)
		}
		_, _ = fmt.Fprintln(os.Stdout, "Auth complete. Token written to", cfg.TokenPath)
		return
	}

	if err := calendar.Run(cfg); err != nil {
		log.Fatalf("run: %v", err)
	}
}
