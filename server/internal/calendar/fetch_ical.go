package calendar

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"slices"
	"strings"
	"time"

	ics "github.com/arran4/golang-ical"
	"github.com/teambition/rrule-go"
)

var errICalBadStatus = errors.New("fetch ical: unexpected HTTP status")

// maxRecurrenceOccurrences caps in-window results per VEVENT to bound memory
// and output size for pathological rules (e.g. FREQ=SECONDLY with no COUNT/UNTIL).
// Pre-window occurrences (before timeMin) are iterated but not counted toward
// the cap, so recurring series with an old DTSTART are not incorrectly truncated.
const maxRecurrenceOccurrences = 100_000

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
	overrides := collectRecurrenceOverrides(cal, loc)
	var out []event
	for _, comp := range cal.Events() {
		ev, ok := parseIcalEvent(comp, loc)
		if !ok {
			continue
		}
		if ev.Title == "" {
			ev.Title = "(no title)"
		}
		out = append(out, expandRecurring(comp, ev, loc, timeMin, timeMax, overrides)...)
	}
	return out
}

// collectRecurrenceOverrides returns a map of UID → original occurrence times for
// every VEVENT that carries a RECURRENCE-ID (i.e. an override for one specific
// slot of a recurring series). The caller uses these to suppress the corresponding
// base-series slots so the display shows the override instead of both.
func collectRecurrenceOverrides(cal *ics.Calendar, loc *time.Location) map[string][]time.Time {
	m := make(map[string][]time.Time)
	for _, comp := range cal.Events() {
		recurProp := comp.GetProperty(ics.ComponentPropertyRecurrenceId)
		if recurProp == nil {
			continue
		}
		uidProp := comp.GetProperty(ics.ComponentPropertyUniqueId)
		if uidProp == nil {
			continue
		}
		// Parse via parseIcalTime (which uses loc for floating datetimes) so the
		// resulting instant matches the rrule-go occurrences generated from DTSTART.
		// golang-ical's GetRecurrenceID uses time.Local for floating datetimes, which
		// produces the wrong UTC instant when time.Local != loc.
		t, _, ok := parseIcalTime(recurProp, loc)
		if !ok {
			continue
		}
		m[uidProp.Value] = append(m[uidProp.Value], t)
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
	rdates := icalEventTimes(comp, ics.ComponentPropertyRdate, loc)
	if rruleProp == nil && len(rdates) == 0 {
		return nonRecurringInWindow(base, timeMin, timeMax)
	}
	var extraExdates []time.Time
	if p := comp.GetProperty(ics.ComponentPropertyUniqueId); p != nil {
		extraExdates = overrides[p.Value]
	}
	set, hasRules := buildRRuleSet(base.Start, rruleProp, rdates, comp, loc, extraExdates)
	if !hasRules {
		// RRULE/RDATE failed to parse; fall back to the single DTSTART occurrence.
		// buildRRuleSet added EXDATEs to the now-discarded set, so re-check them
		// here alongside extraExdates (RECURRENCE-ID overrides).
		exdates := icalEventTimes(comp, ics.ComponentPropertyExdate, loc)
		if slices.ContainsFunc(extraExdates, base.Start.Equal) ||
			slices.ContainsFunc(exdates, base.Start.Equal) {
			return nil
		}
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
func buildRRuleSet(dtstart time.Time, rruleProp *ics.IANAProperty, rdates []time.Time, comp *ics.VEvent, loc *time.Location, extraExdates []time.Time) (rrule.Set, bool) {
	var set rrule.Set
	set.DTStart(dtstart)
	hasRules := false
	if rruleProp != nil {
		// StrToROptionInLocation interprets non-Z UNTIL values in loc rather than
		// UTC, matching the timezone used for DTSTART occurrences.
		if opt, err := rrule.StrToROptionInLocation(rruleProp.Value, loc); err == nil {
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
	for _, t := range icalEventTimes(comp, ics.ComponentPropertyExdate, loc) {
		set.ExDate(t)
	}
	for _, t := range extraExdates {
		set.ExDate(t)
	}
	return set, hasRules
}

// expandOccurrences iterates set for occurrences within [timeMin, timeMax] and
// returns one event instance per occurrence. The iterator is pulled lazily so
// the loop can stop as soon as occ exceeds timeMax. The cap applies only to
// in-window results, so pre-window occurrences from an old DTSTART are iterated
// freely without consuming the cap. The cap guards against pathological rules
// that generate unbounded in-window results (e.g. FREQ=SECONDLY with no COUNT/UNTIL).
func expandOccurrences(set rrule.Set, base event, loc *time.Location, timeMin, timeMax time.Time) []event {
	dur := eventDuration(base)
	next := set.Iterator()
	var out []event

	for {
		occ, more := next()
		if !more {
			break
		}
		if occ.After(timeMax) {
			break
		}
		if inst, inWindow := occurrenceInstance(base, occ, dur, loc, timeMin); inWindow {
			out = append(out, inst)
			if len(out) >= maxRecurrenceOccurrences {
				log.Printf("ical: occurrence cap (%d) reached for %q; truncating expansion", maxRecurrenceOccurrences, base.Title)
				break
			}
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

// icalPropTimes parses all datetime values from a single iCal property into
// []time.Time, anchoring floating datetimes (no TZID, no Z) in loc. The
// property value may be comma-separated (RFC 5545 §3.1). Entries that fail to
// parse are silently skipped. This mirrors parseIcalTime but handles
// comma-separated lists without constructing intermediate struct literals.
func icalPropTimes(prop *ics.IANAProperty, loc *time.Location) []time.Time {
	out := make([]time.Time, 0, strings.Count(prop.Value, ",")+1)
	// Both VALUE and TZID are property-level attributes (RFC 5545 §3.2): they
	// apply uniformly to all comma-separated tokens in prop.Value. Hoist them
	// so neither is re-looked-up on every iteration.
	valueDate := propValueIsDate(prop)
	tzids := prop.ICalParameters["TZID"]
	for raw := range strings.SplitSeq(prop.Value, ",") {
		v := strings.TrimSpace(raw)
		if v == "" {
			continue
		}
		isAllDay := valueDate || !strings.Contains(v, "T")

		var (
			t  time.Time
			ok bool
		)
		if isAllDay {
			var err error
			t, err = time.ParseInLocation("20060102", v, loc)
			ok = err == nil
		} else {
			t, ok = parseIcalDatetime(v, tzids, loc)
		}
		if ok {
			out = append(out, t)
		}
	}
	return out
}

// icalEventTimes returns all datetime values for a given property across all
// occurrences of that property on comp, anchored in loc. Handles repeated
// properties and comma-separated values within each property.
func icalEventTimes(comp *ics.VEvent, which ics.ComponentProperty, loc *time.Location) []time.Time {
	props := comp.GetProperties(which)
	out := make([]time.Time, 0, len(props))
	for _, prop := range props {
		out = append(out, icalPropTimes(prop, loc)...)
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

// propValueIsDate reports whether the VALUE=DATE parameter is explicitly set on
// prop. Extracted from icalPropIsAllDay so it can be hoisted above per-token
// loops (the VALUE parameter is property-level and constant across all tokens).
func propValueIsDate(prop *ics.IANAProperty) bool {
	return slices.ContainsFunc(prop.ICalParameters["VALUE"], func(p string) bool {
		return strings.EqualFold(p, "DATE")
	})
}

// icalPropIsAllDay reports whether a property represents an all-day date.
// True when VALUE=DATE is set explicitly, or when the value has no time component.
func icalPropIsAllDay(prop *ics.IANAProperty, value string) bool {
	return propValueIsDate(prop) || !strings.Contains(value, "T")
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
