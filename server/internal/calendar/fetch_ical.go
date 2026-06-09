package calendar

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	ics "github.com/arran4/golang-ical"
)

var errICalBadStatus = errors.New("fetch ical: unexpected HTTP status")

// fetchEventsIcal fetches the iCal feed at url and returns events in the
// window [now-1h, now+8d]. Google Calendar pre-expands recurring events into
// individual VEVENT instances in the feed, so no client-side RRULE expansion
// is needed.
func fetchEventsIcal(ctx context.Context, url string, loc *time.Location) ([]event, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build ical request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch ical: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: %d", errICalBadStatus, resp.StatusCode)
	}

	cal, err := ics.ParseCalendar(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("parse ical: %w", err)
	}

	now := time.Now().In(loc)
	return eventsFromCal(cal, loc, now.Add(-1*time.Hour), now.Add(8*24*time.Hour)), nil
}

// eventsFromCal extracts events in [timeMin, timeMax] from a parsed calendar.
func eventsFromCal(cal *ics.Calendar, loc *time.Location, timeMin, timeMax time.Time) []event {
	var out []event
	for _, comp := range cal.Events() {
		ev, ok := parseIcalEvent(comp, loc)
		if !ok {
			continue
		}
		if ev.Start.After(timeMax) {
			continue
		}
		end := ev.End
		if end.IsZero() {
			end = ev.Start
		}
		if end.Before(timeMin) {
			continue
		}
		if ev.Title == "" {
			ev.Title = "(no title)"
		}
		out = append(out, ev)
	}
	return out
}

// parseIcalEvent converts a VEVENT component into an event.
// Returns (event, false) if the start time cannot be parsed.
func parseIcalEvent(comp *ics.VEvent, loc *time.Location) (event, bool) {
	title := ""
	if s := comp.GetProperty(ics.ComponentPropertySummary); s != nil {
		title = strings.TrimSpace(s.Value)
	}

	startProp := comp.GetProperty(ics.ComponentPropertyDtStart)
	if startProp == nil {
		return event{Title: "", Start: time.Time{}, End: time.Time{}, AllDay: false}, false
	}

	start, allDay, ok := parseIcalTime(startProp, loc)
	if !ok {
		return event{Title: "", Start: time.Time{}, End: time.Time{}, AllDay: false}, false
	}

	ev := event{Title: title, Start: start, End: time.Time{}, AllDay: allDay}
	if endProp := comp.GetProperty(ics.ComponentPropertyDtEnd); endProp != nil {
		if end, _, ok := parseIcalTime(endProp, loc); ok {
			ev.End = end
		}
	}
	return ev, true
}

// parseIcalTime parses a DTSTART or DTEND iCal property into a time.Time.
// Returns the time, whether it is an all-day date (no time component), and
// whether parsing succeeded.
func parseIcalTime(prop *ics.IANAProperty, fallbackLoc *time.Location) (time.Time, bool, bool) {
	value := strings.TrimSpace(prop.Value)
	if icalPropIsAllDay(prop, value) {
		t, err := time.ParseInLocation("20060102", value, fallbackLoc)
		return t, true, err == nil
	}
	t, ok := parseIcalDatetime(value, prop.ICalParameters["TZID"], fallbackLoc)
	return t, false, ok
}

// icalPropIsAllDay reports whether a property represents an all-day date.
// True when VALUE=DATE is set explicitly, or when the value has no time component.
func icalPropIsAllDay(prop *ics.IANAProperty, value string) bool {
	for _, v := range prop.ICalParameters["VALUE"] {
		if strings.EqualFold(v, "DATE") {
			return true
		}
	}
	return !strings.Contains(value, "T")
}

// parseIcalDatetime parses a DATETIME value (not all-day).
// UTC datetimes end with 'Z'; local datetimes use the TZID from tzids (first
// entry), falling back to fallbackLoc when absent or unrecognised.
func parseIcalDatetime(value string, tzids []string, fallbackLoc *time.Location) (time.Time, bool) {
	if strings.HasSuffix(value, "Z") {
		t, err := time.Parse("20060102T150405Z", value)
		return t.In(fallbackLoc), err == nil
	}
	loc := fallbackLoc
	if len(tzids) > 0 {
		if l, err := time.LoadLocation(tzids[0]); err == nil {
			loc = l
		}
	}
	t, err := time.ParseInLocation("20060102T150405", value, loc)
	return t.In(fallbackLoc), err == nil
}
