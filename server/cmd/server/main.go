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
	defaultFetchInterval = 10 * time.Minute
)

func main() {
	var cfg calendar.Config
	var showVersion bool

	flag.BoolVar(&showVersion, "version", false, "Print version and exit")
	flag.StringVar(&cfg.ListenAddr, "listen", ":8080", "HTTP listen address (use 127.0.0.1:PORT to restrict to loopback)")
	flag.StringVar(&cfg.ICalURL, "ical-url", "", "Secret iCal URL from Google Calendar settings")
	flag.StringVar(&cfg.Timezone, "tz", "America/Los_Angeles", "IANA timezone, e.g. America/New_York")
	flag.DurationVar(&cfg.FetchInterval, "fetch-interval", defaultFetchInterval, "How often to poll the iCal feed")
	flag.Parse()

	if showVersion {
		_, _ = fmt.Fprintln(os.Stdout, version)
		return
	}

	if cfg.ICalURL == "" {
		cfg.ICalURL = os.Getenv("ICAL_URL")
	}

	log.Printf("calendar-server %s starting", version)

	if err := calendar.Run(cfg); err != nil {
		log.Fatalf("run: %v", err)
	}
}
