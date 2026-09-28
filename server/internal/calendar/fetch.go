package calendar

import "time"

const fetchTimeout = 30 * time.Second

// event is the internal representation of a calendar entry, normalized to
// the configured local timezone and with all-day events flagged.
type event struct {
	Start  time.Time
	End    time.Time
	Title  string
	AllDay bool
}
