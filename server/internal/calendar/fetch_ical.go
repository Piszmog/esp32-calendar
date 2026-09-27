package calendar

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	ics "github.com/arran4/golang-ical"
	"github.com/teambition/rrule-go"
)

var (
	errICalBadStatus = errors.New("fetch ical: unexpected HTTP status")
	errICalTooLarge  = errors.New("fetch ical: feed exceeds size limit")
)

const (
	icalUserAgent = "calendar-display"
	maxICalBytes  = 10 << 20
	hoursPerDay   = 24
	daysPerWeek   = 7
)

// icalDurationRe matches an RFC 5545 dur-value: [+-]P then weeks alone, or
// days and/or a T-prefixed hours/minutes/seconds part.
var icalDurationRe = regexp.MustCompile(`^([+-])?P(?:(\d+)W|(?:(\d+)D)?(?:T(?:(\d+)H)?(?:(\d+)M)?(?:(\d+)S)?)?)$`)

// redactURL strips the request URL from net/http errors. The iCal URL is a
// bearer token and must not reach logs or /healthz.
func redactURL(err error) error {
	if uerr, ok := errors.AsType[*url.Error](err); ok {
		return fmt.Errorf("%s: %w", uerr.Op, uerr.Err)
	}
	return err
}

// fetchEventsIcal fetches the iCal feed at feedURL and returns events in the
// window [now-1h, now+8d]. Recurring events (RRULE/RDATE) are expanded
// client-side by rrule-go; EXDATE exclusions are honoured.
func fetchEventsIcal(ctx context.Context, feedURL string, loc *time.Location) ([]event, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feedURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build ical request: %w", redactURL(err))
	}
	req.Header.Set("User-Agent", icalUserAgent)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch ical: %w", redactURL(err))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: %d", errICalBadStatus, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxICalBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read ical: %w", err)
	}
	if len(body) > maxICalBytes {
		return nil, fmt.Errorf("%w (%d bytes)", errICalTooLarge, maxICalBytes)
	}

	cal, err := ics.ParseCalendar(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("parse ical: %w", err)
	}

	now := time.Now().In(loc)
	return eventsFromCal(cal, loc, now.Add(-1*time.Hour), now.Add(8*24*time.Hour)), nil
}

// eventsFromCal extracts events in [timeMin, timeMax] from a parsed calendar,
// expanding recurring events (RRULE/RDATE) into individual instances.
func eventsFromCal(cal *ics.Calendar, loc *time.Location, timeMin, timeMax time.Time) []event {
	overrides := collectRecurrenceOverrides(cal, loc)
	var out []event
	for _, comp := range cal.Events() {
		// Cancelled overrides still feed collectRecurrenceOverrides above, so
		// the base-series slot they replace stays suppressed.
		if isCancelled(comp) {
			continue
		}
		ev, ok := parseIcalEvent(comp, loc)
		if !ok {
			continue
		}
		if ev.Title == "" {
			ev.Title = "(no title)"
		}
		for _, inst := range expandRecurring(comp, ev, loc, timeMin, timeMax, overrides) {
			out = append(out, inLoc(inst, loc))
		}
	}
	return out
}

// inLoc returns ev with its times in loc. Events are parsed in their own zone
// so recurrences expand on the right wall clock; the rest of the package
// expects loc.
func inLoc(ev event, loc *time.Location) event {
	ev.Start = ev.Start.In(loc)
	if !ev.End.IsZero() {
		ev.End = ev.End.In(loc)
	}
	return ev
}

// anchorFloating re-reads a time golang-ical parsed in time.Local — a
// floating datetime or a DATE value — as the same wall clock in loc, which is
// how parseIcalTime reads DTSTART. Times with a TZID or Z are returned as-is.
func anchorFloating(t time.Time, loc *time.Location) time.Time {
	if t.Location() != time.Local {
		return t
	}
	y, mo, d := t.Date()
	h, mi, sec := t.Clock()
	return time.Date(y, mo, d, h, mi, sec, t.Nanosecond(), loc)
}

// isCancelled reports whether the VEVENT has STATUS:CANCELLED.
func isCancelled(comp *ics.VEvent) bool {
	p := comp.GetProperty(ics.ComponentPropertyStatus)
	return p != nil && strings.EqualFold(strings.TrimSpace(p.Value), "CANCELLED")
}

// collectRecurrenceOverrides returns a map of UID → original occurrence times for
// every VEVENT that carries a RECURRENCE-ID (i.e. an override for one specific
// slot of a recurring series). The caller uses these to suppress the corresponding
// base-series slots so the display shows the override instead of both.
func collectRecurrenceOverrides(cal *ics.Calendar, loc *time.Location) map[string][]time.Time {
	m := make(map[string][]time.Time)
	for _, comp := range cal.Events() {
		if comp.GetProperty(ics.ComponentPropertyRecurrenceId) == nil {
			continue
		}
		uidProp := comp.GetProperty(ics.ComponentPropertyUniqueId)
		if uidProp == nil {
			continue
		}
		t, err := comp.GetRecurrenceID()
		if err != nil {
			continue
		}
		m[uidProp.Value] = append(m[uidProp.Value], anchorFloating(t, loc))
	}
	return m
}

// expandRecurring expands ev into all occurrences within [timeMin, timeMax].
// For non-recurring events it performs the same window check as before. For
// recurring events (RRULE and/or RDATE) it uses rrule-go and honours EXDATE.
// overrides maps UID → original occurrence times that have been overridden by a
// RECURRENCE-ID VEVENT and must be excluded from the base-series expansion.
func expandRecurring(comp *ics.VEvent, base event, loc *time.Location, timeMin, timeMax time.Time, overrides map[string][]time.Time) []event {
	rruleProp := comp.GetProperty(ics.ComponentPropertyRrule)
	rdates, _ := comp.GetRDates()
	for i, t := range rdates {
		rdates[i] = anchorFloating(t, loc)
	}
	if rruleProp == nil && len(rdates) == 0 {
		return nonRecurringInWindow(base, timeMin, timeMax)
	}
	var extraExdates []time.Time
	if p := comp.GetProperty(ics.ComponentPropertyUniqueId); p != nil {
		extraExdates = overrides[p.Value]
	}
	set, hasRules := buildRRuleSet(base.Start, rruleProp, rdates, comp, extraExdates, loc)
	if !hasRules {
		// RRULE/RDATE failed to parse; fall back to the single DTSTART occurrence
		// so the event is visible rather than silently disappearing.
		return nonRecurringInWindow(base, timeMin, timeMax)
	}
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
// and EXDATE properties, plus any extra EXDATE times from RECURRENCE-ID overrides.
// Returns the set and whether at least one rule or RDATE was successfully added
// (false means the RRULE failed to parse and no RDATEs exist — caller should fall back).
func buildRRuleSet(dtstart time.Time, rruleProp *ics.IANAProperty, rdates []time.Time, comp *ics.VEvent, extraExdates []time.Time, loc *time.Location) (rrule.Set, bool) {
	var set rrule.Set
	set.DTStart(dtstart)
	hasRules := false
	if rruleProp != nil {
		if opt, err := rrule.StrToROption(rruleProp.Value); err == nil {
			opt.Dtstart = dtstart
			if r, err2 := rrule.NewRRule(*opt); err2 == nil {
				set.RRule(r)
				hasRules = true
			}
		}
	}
	for _, t := range rdates {
		set.RDate(t)
		hasRules = true
	}
	if exdates, err := comp.GetExDates(); err == nil {
		for _, t := range exdates {
			set.ExDate(anchorFloating(t, loc))
		}
	}
	for _, t := range extraExdates {
		set.ExDate(t)
	}
	return set, hasRules
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
	switch {
	case base.AllDay && dur > 0:
		// Count whole days, not hours: a 23h/25h DST day on either the base
		// event or this occurrence would otherwise shift the end off midnight.
		inst.End = inst.Start.AddDate(0, 0, int(math.Round(dur.Hours()/hoursPerDay)))
	case dur > 0:
		inst.End = occ.Add(dur).In(loc)
	default:
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
		// Collapse whitespace: golang-ical unescapes \n into a real newline,
		// which would render as a missing glyph.
		title = strings.Join(strings.Fields(s.Value), " ")
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
	} else if durProp := comp.GetProperty(ics.ComponentPropertyDuration); durProp != nil {
		if days, clock, ok := parseIcalDuration(strings.TrimSpace(durProp.Value)); ok {
			// Day parts are nominal (wall-clock) per RFC 5545, so DST days
			// don't shift all-day ends off midnight.
			ev.End = start.AddDate(0, 0, days).Add(clock)
		}
	}
	return ev, true
}

// parseIcalDuration parses an RFC 5545 DURATION value into whole days (weeks
// included) and a clock duration. Returns ok=false for malformed values,
// including "P" or "PT" with no components.
func parseIcalDuration(s string) (int, time.Duration, bool) {
	m := icalDurationRe.FindStringSubmatch(s)
	if m == nil || strings.HasSuffix(s, "P") || strings.HasSuffix(s, "T") {
		return 0, 0, false
	}
	num := func(v string) int {
		n, _ := strconv.Atoi(v)
		return n
	}
	days := num(m[2])*daysPerWeek + num(m[3])
	clock := time.Duration(num(m[4]))*time.Hour +
		time.Duration(num(m[5]))*time.Minute +
		time.Duration(num(m[6]))*time.Second
	if m[1] == "-" {
		days, clock = -days, -clock
	}
	return days, clock, true
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

// parseIcalDatetime parses a DATETIME value (not all-day) in its own zone,
// so RRULE expansion follows that zone's DST. UTC datetimes end with 'Z';
// local datetimes use the TZID from tzids (first entry), falling back to
// fallbackLoc when absent or unrecognised.
func parseIcalDatetime(value string, tzids []string, fallbackLoc *time.Location) (time.Time, bool) {
	if strings.HasSuffix(value, "Z") {
		t, err := time.Parse("20060102T150405Z", value)
		return t, err == nil
	}
	loc := fallbackLoc
	if len(tzids) > 0 {
		if l, err := time.LoadLocation(tzids[0]); err == nil {
			loc = l
		}
	}
	t, err := time.ParseInLocation("20060102T150405", value, loc)
	return t, err == nil
}
