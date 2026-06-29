package calendar

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	ics "github.com/arran4/golang-ical"
	"github.com/teambition/rrule-go"
)

var errICalBadStatus = errors.New("fetch ical: unexpected HTTP status")

// fetchEventsIcal fetches the iCal feed at url and returns events in the
// window [now-1h, now+8d]. Recurring events (RRULE/RDATE) are expanded
// client-side by rrule-go; EXDATE exclusions are honoured.
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

// eventsFromCal extracts events in [timeMin, timeMax] from a parsed calendar,
// expanding recurring events (RRULE/RDATE) into individual instances.
func eventsFromCal(cal *ics.Calendar, loc *time.Location, timeMin, timeMax time.Time) []event {
	var out []event
	for _, comp := range cal.Events() {
		ev, ok := parseIcalEvent(comp, loc)
		if !ok {
			continue
		}
		if ev.Title == "" {
			ev.Title = "(no title)"
		}
		out = append(out, expandRecurring(comp, ev, loc, timeMin, timeMax)...)
	}
	return out
}

// expandRecurring expands ev into all occurrences within [timeMin, timeMax].
// For non-recurring events it performs the same window check as before. For
// recurring events (RRULE and/or RDATE) it uses rrule-go and honours EXDATE.
func expandRecurring(comp *ics.VEvent, base event, loc *time.Location, timeMin, timeMax time.Time) []event {
	rruleProp := comp.GetProperty(ics.ComponentPropertyRrule)
	rdates, _ := comp.GetRDates()
	if rruleProp == nil && len(rdates) == 0 {
		return nonRecurringInWindow(base, timeMin, timeMax)
	}
	set := buildRRuleSet(base.Start, rruleProp, rdates, comp)
	return expandOccurrences(set, base, loc, timeMin, timeMax)
}

// nonRecurringInWindow returns the event when it overlaps [timeMin, timeMax],
// or nil when it falls outside. Mirrors the original eventsFromCal window check.
func nonRecurringInWindow(base event, timeMin, timeMax time.Time) []event {
	if base.Start.After(timeMax) {
		return nil
	}
	end := base.End
	if end.IsZero() {
		end = base.Start
	}
	if end.Before(timeMin) {
		return nil
	}
	return []event{base}
}

// buildRRuleSet assembles an rrule.Set from the event's DTSTART, RRULE, RDATE,
// and EXDATE properties.
func buildRRuleSet(dtstart time.Time, rruleProp *ics.IANAProperty, rdates []time.Time, comp *ics.VEvent) rrule.Set {
	var set rrule.Set
	set.DTStart(dtstart)
	if rruleProp != nil {
		if opt, err := rrule.StrToROption(rruleProp.Value); err == nil {
			opt.Dtstart = dtstart
			if r, err2 := rrule.NewRRule(*opt); err2 == nil {
				set.RRule(r)
			}
		}
	}
	for _, t := range rdates {
		set.RDate(t)
	}
	if exdates, err := comp.GetExDates(); err == nil {
		for _, t := range exdates {
			set.ExDate(t)
		}
	}
	return set
}

// expandOccurrences queries set for occurrences within [timeMin, timeMax] and
// returns one event instance per occurrence.
func expandOccurrences(set rrule.Set, base event, loc *time.Location, timeMin, timeMax time.Time) []event {
	dur := eventDuration(base)
	// Widen the lower query bound by dur so an occurrence that started just
	// before timeMin but is still ongoing is included.
	queryMin := timeMin
	if dur > 0 {
		queryMin = timeMin.Add(-dur)
	}
	var out []event
	for _, occ := range set.Between(queryMin, timeMax, true) {
		if inst, ok := occurrenceInstance(base, occ, dur, loc, timeMin); ok {
			out = append(out, inst)
		}
	}
	return out
}

// eventDuration returns the duration of ev, or 0 for zero-end-time events.
func eventDuration(ev event) time.Duration {
	if ev.End.IsZero() {
		return 0
	}
	return ev.End.Sub(ev.Start)
}

// occurrenceInstance builds one event instance for a recurrence occurrence time.
// Returns (inst, false) when the occurrence ends before timeMin (widened-query tail).
func occurrenceInstance(base event, occ time.Time, dur time.Duration, loc *time.Location, timeMin time.Time) (event, bool) {
	inst := base
	inst.Start = occ.In(loc)
	if dur > 0 {
		inst.End = occ.Add(dur).In(loc)
	} else {
		inst.End = time.Time{}
	}
	occEnd := inst.End
	if occEnd.IsZero() {
		occEnd = inst.Start
	}
	if occEnd.Before(timeMin) {
		return inst, false
	}
	return inst, true
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
