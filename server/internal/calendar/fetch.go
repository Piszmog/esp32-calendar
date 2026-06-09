package calendar

import (
	"context"
	"time"
)

const fetchTimeout = 30 * time.Second

// event is the internal representation of a calendar entry, normalized to
// the configured local timezone and with all-day events flagged.
type event struct {
	Start  time.Time
	End    time.Time
	Title  string
	AllDay bool
}

// fetchEvents returns events from now-1h to now+8d via the configured iCal feed.
func fetchEvents(ctx context.Context, c Config, loc *time.Location) ([]event, error) {
	return fetchEventsIcal(ctx, c.ICalURL, loc)
}
