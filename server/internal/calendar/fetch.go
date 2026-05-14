package calendar

import (
	"context"
	"fmt"
	"time"

	gcalendar "google.golang.org/api/calendar/v3"
	"google.golang.org/api/option"
)

// event is the internal representation of a calendar entry, normalized to
// the configured local timezone and with all-day events flagged.
type event struct {
	Start  time.Time
	End    time.Time
	Title  string
	AllDay bool
}

const (
	maxEventResults = 250
	fetchTimeout    = 30 * time.Second
)

// fetchEvents returns events from now-1h to now+8d, single-occurrence expanded,
// in the configured timezone.
func fetchEvents(ctx context.Context, c Config, loc *time.Location) ([]event, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	ts, err := tokenSource(ctx, c)
	if err != nil {
		return nil, err
	}
	opts := append([]option.ClientOption{option.WithTokenSource(ts)}, c.clientOpts...)
	srv, err := gcalendar.NewService(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("calendar service: %w", err)
	}

	now := time.Now().In(loc)
	timeMin := now.Add(-1 * time.Hour).Format(time.RFC3339)
	timeMax := now.Add(8 * 24 * time.Hour).Format(time.RFC3339)

	items, err := srv.Events.List(c.CalendarID).
		Context(ctx).
		TimeMin(timeMin).
		TimeMax(timeMax).
		SingleEvents(true).
		OrderBy("startTime").
		MaxResults(maxEventResults).
		Do()
	if err != nil {
		return nil, fmt.Errorf("events.list: %w", err)
	}

	out := make([]event, 0, len(items.Items))
	for _, it := range items.Items {
		ev, ok := parseEventTime(it, loc)
		if !ok {
			continue
		}
		if ev.Title == "" {
			ev.Title = "(no title)"
		}
		out = append(out, ev)
	}
	return out, nil
}

// parseEventTime normalises a Calendar API item into an event.
// Returns (event, false) if the item has no parseable start time.
func parseEventTime(it *gcalendar.Event, loc *time.Location) (event, bool) {
	switch {
	case it.Start.DateTime != "":
		return parseTimedEvent(it, loc)
	case it.Start.Date != "":
		return parseAllDayEvent(it, loc)
	default:
		return event{Title: "", Start: time.Time{}, End: time.Time{}, AllDay: false}, false
	}
}

func parseTimedEvent(it *gcalendar.Event, loc *time.Location) (event, bool) {
	t, err := time.Parse(time.RFC3339, it.Start.DateTime)
	if err != nil {
		return event{Title: "", Start: time.Time{}, End: time.Time{}, AllDay: false}, false
	}
	ev := event{
		Title:  it.Summary,
		Start:  t.In(loc),
		End:    time.Time{},
		AllDay: false,
	}
	if it.End != nil && it.End.DateTime != "" {
		if e, err := time.Parse(time.RFC3339, it.End.DateTime); err == nil {
			ev.End = e.In(loc)
		}
	}
	return ev, true
}

func parseAllDayEvent(it *gcalendar.Event, loc *time.Location) (event, bool) {
	t, err := time.ParseInLocation("2006-01-02", it.Start.Date, loc)
	if err != nil {
		return event{Title: "", Start: time.Time{}, End: time.Time{}, AllDay: false}, false
	}
	ev := event{
		Title:  it.Summary,
		Start:  t,
		End:    time.Time{},
		AllDay: true,
	}
	if it.End != nil && it.End.Date != "" {
		if e, err := time.ParseInLocation("2006-01-02", it.End.Date, loc); err == nil {
			ev.End = e
		}
	}
	return ev, true
}
