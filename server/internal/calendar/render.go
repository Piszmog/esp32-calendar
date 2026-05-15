package calendar

import (
	_ "embed"
	"fmt"
	"image"
	"image/png"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fogleman/gg"
	"github.com/golang/freetype/truetype"
	"golang.org/x/image/font"
)

const (
	imgW = 800
	imgH = 480

	// Header constants.
	headerH       = 50.0
	headerPadX    = 22.0
	headerPadY    = 10.0
	headerLineW   = 2.0
	contentTopGap = 12.0

	// Column layout — package-level so helper functions can share them.
	leftX       = 20.0
	leftW       = 400.0
	colGap      = 14.0
	rightX      = leftX + leftW + colGap
	rightMargin = 20.0
	rightW      = float64(imgW) - rightX - rightMargin

	// Font sizes.
	fontSizeHeader      = 26.0
	fontSizeSectionHead = 20.0
	fontSizeBody        = 20.0
	fontSizeWeekDay     = 18.0
	fontSizeWeekSummary = 18.0
	fontSizeWeekMore    = 14.0
	fontSizeFooter      = 14.0
	fontDPI             = 72.0

	// Chip (event pill) geometry.
	chipH             = 40.0
	chipGap           = 4.0
	chipCorner        = 4.0
	chipTimeX         = 12.0
	chipTitleX        = 96.0
	chipTextOffY      = 9.0
	chipTitleRightPad = 12.0
	todayChipLineW    = 3.0
	tomorrowChipLineW = 1.0

	// Section spacing.
	sectionGapY      = 30.0
	sectionHeadRuleY = 24.0
	tomorrowGap      = 10.0
	weekHeaderGap    = 30.0
	weekRowH        = 64.0
	weekTextOffY    = 6.0
	weekDashOffY    = 32.0
	weekSummaryOffY = 30.0
	weekMoreOffY    = 50.0

	// Footer constants.
	footerH           = 32.0
	footerPadX        = 22.0
	footerWifiOffY    = 8.0
	footerBatteryOffY = 9.0
	footerRightPad    = 22.0
	wifiBarW          = 4.0 // matches icons.go barW
	wifiBarGap        = 2.0 // matches icons.go gap
	wifiBars          = 4   // matches icons.go bars
	wifiAfterGap      = 14.0
	wifiIconAdv       = wifiBars*(wifiBarW+wifiBarGap) + wifiAfterGap
	batteryBodyW      = 34.0 // matches icons.go bodyW
	batteryAfterGap   = 3.0
	batteryLabelGap   = 6.0
	batteryIconAdv    = batteryBodyW + batteryAfterGap + batteryLabelGap

	// Content limits used in summarizeDay.
	eventListTitleMax = 10
	summaryMax        = 60
	oneDayDuration    = 24 * time.Hour

	// WiFi bar counts for rssiToBars.
	wifiFullBars = 4
	wifiGoodBars = 3
	wifiFairBars = 2
	wifiWeakBars = 1
)

//go:embed fonts/DejaVuSans.ttf
var fontRegularBytes []byte

//go:embed fonts/DejaVuSans-Bold.ttf
var fontBoldBytes []byte

var loadFonts = sync.OnceValues(func() (*truetype.Font, *truetype.Font) {
	r, err := truetype.Parse(fontRegularBytes)
	if err != nil {
		panic(fmt.Errorf("parse regular font: %w", err))
	}
	b, err := truetype.Parse(fontBoldBytes)
	if err != nil {
		panic(fmt.Errorf("parse bold font: %w", err))
	}
	return r, b
})

func face(size float64, bold bool) font.Face {
	regular, boldFont := loadFonts()
	f := regular
	if bold {
		f = boldFont
	}
	return truetype.NewFace(f, &truetype.Options{Size: size, DPI: fontDPI, Hinting: font.HintingFull})
}

// displayData is what the renderer consumes — the result of running raw
// calendar events through filtering and grouping logic.
type displayData struct {
	Now        time.Time
	Today      []event
	Tomorrow   []event
	WeekAhead  []daySummary
	BatteryPct int // -1 = unknown (renderer hides the icon)
	WifiSignal int // 0-4 bars
}

type daySummary struct {
	Date    time.Time
	Summary string // formatted line, empty if no events
	More    string // overflow count line, e.g. "+ 3 more events"; empty when no overflow
}

// daysBetween returns the number of calendar days from a to b.
// Noon UTC as a fixed reference avoids the 23h/25h DST gap between adjacent
// local midnights (e.g. spring-forward yields 23h between consecutive midnights,
// which int(hours/24) would truncate to 0 instead of 1).
func daysBetween(a, b time.Time) int {
	aD := time.Date(a.Year(), a.Month(), a.Day(), 12, 0, 0, 0, time.UTC)
	bD := time.Date(b.Year(), b.Month(), b.Day(), 12, 0, 0, 0, time.UTC)
	return int(bD.Sub(aD) / oneDayDuration)
}

// allDaySpan returns the [start, end) day boundaries for ev in loc.
// End is exclusive per Google Calendar convention; defaults to start+1d when absent.
func allDaySpan(ev event, loc *time.Location) (time.Time, time.Time) {
	start := time.Date(ev.Start.Year(), ev.Start.Month(), ev.Start.Day(), 0, 0, 0, 0, loc)
	end := start.AddDate(0, 0, 1)
	if !ev.End.IsZero() {
		end = time.Date(ev.End.Year(), ev.End.Month(), ev.End.Day(), 0, 0, 0, 0, loc)
	}
	return start, end
}

// spansDay reports whether the half-open interval [start, end) covers day.
func spansDay(start, end, day time.Time) bool {
	return !start.After(day) && day.Before(end)
}

// buildDisplayData groups events into the layout's three sections.
func buildDisplayData(events []event, loc *time.Location, batPct, rssi int, now time.Time) displayData {
	now = now.In(loc)
	year, month, day := now.Date()
	startOfToday := time.Date(year, month, day, 0, 0, 0, 0, loc)
	tomorrow := startOfToday.AddDate(0, 0, 1)

	d := displayData{
		Now:        now,
		Today:      nil,
		Tomorrow:   nil,
		WeekAhead:  nil,
		BatteryPct: batPct,
		WifiSignal: rssiToBars(rssi),
	}

	// Past-event cutoff: hide timed events that started more than 30 min ago.
	cutoff := now.Add(-30 * time.Minute)
	bucketTodayTomorrow(events, &d, startOfToday, tomorrow, cutoff, loc)
	d.WeekAhead = buildWeekAhead(events, startOfToday, loc)
	return d
}

func bucketTodayTomorrow(events []event, d *displayData, startOfToday, tomorrow, cutoff time.Time, loc *time.Location) {
	for _, ev := range events {
		if ev.AllDay {
			start, end := allDaySpan(ev, loc)
			if spansDay(start, end, startOfToday) {
				d.Today = append(d.Today, ev)
			}
			if spansDay(start, end, tomorrow) {
				d.Tomorrow = append(d.Tomorrow, ev)
			}
			continue
		}
		evDay := time.Date(ev.Start.Year(), ev.Start.Month(), ev.Start.Day(), 0, 0, 0, 0, loc)
		switch daysBetween(startOfToday, evDay) {
		case 0:
			if ev.Start.After(cutoff) {
				d.Today = append(d.Today, ev)
			}
		case 1:
			d.Tomorrow = append(d.Tomorrow, ev)
		}
	}
	sort.Slice(d.Tomorrow, func(i, j int) bool { return d.Tomorrow[i].Start.Before(d.Tomorrow[j].Start) })
}

func buildWeekAhead(events []event, startOfToday time.Time, loc *time.Location) []daySummary {
	weekDays := make(map[int][]event)
	for _, ev := range events {
		if ev.AllDay {
			start, end := allDaySpan(ev, loc)
			for offset := 2; offset <= 6; offset++ {
				if spansDay(start, end, startOfToday.AddDate(0, 0, offset)) {
					weekDays[offset] = append(weekDays[offset], ev)
				}
			}
			continue
		}
		evDay := time.Date(ev.Start.Year(), ev.Start.Month(), ev.Start.Day(), 0, 0, 0, 0, loc)
		if n := daysBetween(startOfToday, evDay); n >= 2 && n <= 6 {
			weekDays[n] = append(weekDays[n], ev)
		}
	}

	var week []daySummary
	for offset := 2; offset <= 6; offset++ {
		day := startOfToday.AddDate(0, 0, offset)
		entries := weekDays[offset]
		sort.Slice(entries, func(i, j int) bool { return entries[i].Start.Before(entries[j].Start) })
		summary, more := summarizeDay(entries)
		week = append(week, daySummary{
			Date:    day,
			Summary: summary,
			More:    more,
		})
	}
	return week
}

// summarizeDay collapses a day's events to a compact one-liner plus an
// optional overflow indicator when events don't all fit.
//
//	one event:  "10:00  Project sync", ""
//	many fit:   "9 Standup · 14 1:1 · 16 Demo", ""
//	overflow:   "9 Standup · 14 1:1", "+ 2 more events"
//	all-day:    "All-day company offsite", ""
func summarizeDay(events []event) (string, string) {
	if len(events) == 0 {
		return "", ""
	}
	if len(events) == 1 {
		ev := events[0]
		if ev.AllDay {
			return ev.Title, ""
		}
		return ev.Start.Format("15:04") + "  " + ev.Title, ""
	}
	parts := make([]string, 0, len(events))
	for _, ev := range events {
		if ev.AllDay {
			parts = append(parts, ev.Title)
			continue
		}
		// Short time: drop trailing :00 minutes and leading zero
		t := strings.TrimSuffix(ev.Start.Format("15:04"), ":00")
		t = strings.TrimPrefix(t, "0")
		// Truncate title aggressively when many events on a day
		parts = append(parts, t+" "+truncate(ev.Title, eventListTitleMax))
	}
	out := strings.Join(parts, " · ")
	if len([]rune(out)) <= summaryMax {
		return out, ""
	}
	// Drop events from the tail until the kept portion fits within summaryMax,
	// then report how many were dropped on a separate line.
	for k := len(parts) - 1; k >= 1; k-- {
		candidate := strings.Join(parts[:k], " · ")
		if len([]rune(candidate)) <= summaryMax {
			return candidate, moreSuffix(len(parts) - k)
		}
	}
	// Pathological: even the first part alone overflows; fall back to truncation.
	return truncate(out, summaryMax), ""
}

func moreSuffix(n int) string {
	if n == 1 {
		return "+ 1 more event"
	}
	return fmt.Sprintf("+ %d more events", n)
}

// rssiToBars maps WiFi RSSI dBm to 0..4 bars.
// Values >= 0 (including the rssiUnknown sentinel) return 0 bars.
func rssiToBars(rssi int) int {
	switch {
	case rssi >= 0:
		return 0
	case rssi >= -55:
		return wifiFullBars
	case rssi >= -65:
		return wifiGoodBars
	case rssi >= -75:
		return wifiFairBars
	case rssi >= -85:
		return wifiWeakBars
	default:
		return 0
	}
}

// renderImage produces the 800x480 RGBA image of the calendar.
func renderImage(d displayData) (image.Image, error) {
	dc := gg.NewContext(imgW, imgH)
	dc.SetRGB(1, 1, 1)
	dc.Clear()
	dc.SetRGB(0, 0, 0)

	contentTop := drawHeader(dc, d.Now)
	y := drawTodayPanel(dc, d, contentTop)
	drawTomorrowPanel(dc, d, y)
	drawWeekAheadPanel(dc, d, contentTop)
	drawFooterPanel(dc, d)

	return dc.Image(), nil
}

func drawHeader(dc *gg.Context, now time.Time) float64 {
	dc.SetFontFace(face(fontSizeHeader, true))
	drawTopLeft(dc, now.Format("Monday, January 2"), headerPadX, headerPadY)
	dc.SetLineWidth(headerLineW)
	dc.DrawLine(0, headerH, imgW, headerH)
	dc.Stroke()
	return headerH + contentTopGap
}

func drawTodayPanel(dc *gg.Context, d displayData, startY float64) float64 {
	y := startY
	dc.SetFontFace(face(fontSizeSectionHead, true))
	drawTopLeft(dc, "TODAY", leftX, y)
	dc.SetLineWidth(1)
	dc.DrawLine(leftX, y+sectionHeadRuleY, leftX+leftW, y+sectionHeadRuleY)
	dc.Stroke()
	y += sectionGapY
	if len(d.Today) == 0 {
		dc.SetFontFace(face(fontSizeBody, false))
		drawTopLeft(dc, "Nothing else scheduled today", leftX, y+chipTextOffY)
		y += chipH + chipGap
	} else {
		for _, ev := range d.Today {
			drawChip(dc, leftX, y, leftW, chipH, ev, todayChipLineW)
			y += chipH + chipGap
		}
	}
	return y + tomorrowGap
}

func drawTomorrowPanel(dc *gg.Context, d displayData, startY float64) {
	y := startY
	dc.SetFontFace(face(fontSizeSectionHead, true))
	drawTopLeft(dc, "TOMORROW", leftX, y)
	dc.SetLineWidth(1)
	dc.DrawLine(leftX, y+sectionHeadRuleY, leftX+leftW, y+sectionHeadRuleY)
	dc.Stroke()
	y += sectionGapY
	for _, ev := range d.Tomorrow {
		drawChip(dc, leftX, y, leftW, chipH, ev, tomorrowChipLineW)
		y += chipH + chipGap
	}
	if len(d.Tomorrow) == 0 {
		dc.SetFontFace(face(fontSizeBody, false))
		drawTopLeft(dc, "Nothing scheduled tomorrow", leftX, y+chipTextOffY)
	}
}

func drawWeekAheadPanel(dc *gg.Context, d displayData, contentTop float64) {
	y := contentTop
	dc.SetFontFace(face(fontSizeSectionHead, true))
	drawTopLeft(dc, "WEEK AHEAD", rightX, y)
	y += weekHeaderGap
	dc.SetLineWidth(1)
	for _, ds := range d.WeekAhead {
		dc.DrawLine(rightX, y, rightX+rightW, y)
		dc.Stroke()
		dc.SetFontFace(face(fontSizeWeekDay, true))
		label := strings.ToUpper(ds.Date.Format("Mon")) + " " + ds.Date.Format("2")
		drawTopLeft(dc, label, rightX, y+weekTextOffY)
		dc.SetFontFace(face(fontSizeWeekSummary, true))
		if ds.Summary == "" {
			drawTopLeft(dc, "—", rightX, y+weekDashOffY)
		} else {
			drawTopLeft(dc, truncateToWidth(dc, ds.Summary, rightW), rightX, y+weekSummaryOffY)
		}
		if ds.More != "" {
			dc.SetFontFace(face(fontSizeWeekMore, false))
			drawTopLeft(dc, truncateToWidth(dc, ds.More, rightW), rightX, y+weekMoreOffY)
		}
		y += weekRowH
	}
	dc.DrawLine(rightX, y, rightX+rightW, y)
	dc.Stroke()
}

func drawFooterPanel(dc *gg.Context, d displayData) {
	footerTop := float64(imgH) - footerH
	dc.SetLineWidth(1)
	dc.DrawLine(0, footerTop, float64(imgW), footerTop)
	dc.Stroke()

	fx := footerPadX
	drawWifi(dc, fx, footerTop+footerWifiOffY, d.WifiSignal)
	fx += wifiIconAdv
	if d.BatteryPct >= 0 {
		drawBattery(dc, fx, footerTop+footerBatteryOffY, d.BatteryPct)
		fx += batteryIconAdv
		dc.SetFontFace(face(fontSizeFooter, true))
		drawTopLeft(dc, fmt.Sprintf("%d%%", d.BatteryPct), fx, footerTop+footerWifiOffY)
	}

	dc.SetFontFace(face(fontSizeFooter, false))
	updStr := d.Now.Format("Updated 15:04")
	updW, _ := dc.MeasureString(updStr)
	drawTopLeft(dc, updStr, float64(imgW)-updW-footerRightPad, footerTop+footerWifiOffY)
}

// drawTopLeft draws text with (x,y) interpreted as the top-left of the
// text bounding box (the PIL convention), not gg's default baseline-left.
func drawTopLeft(dc *gg.Context, s string, x, y float64) {
	dc.DrawStringAnchored(s, x, y, 0, 1)
}

func drawChip(dc *gg.Context, x, y, w, h float64, ev event, lineW float64) {
	dc.SetRGB(0, 0, 0)
	dc.SetLineWidth(lineW)
	dc.DrawRoundedRectangle(x, y, w, h, chipCorner)
	dc.Stroke()
	dc.SetFontFace(face(fontSizeBody, true))
	drawTopLeft(dc, chipTimeString(ev), x+chipTimeX, y+chipTextOffY)
	dc.SetFontFace(face(fontSizeBody, false))
	drawTopLeft(dc, truncateToWidth(dc, ev.Title, w-chipTitleX-chipTitleRightPad), x+chipTitleX, y+chipTextOffY)
}

func chipTimeString(ev event) string {
	if ev.AllDay {
		return "all-day"
	}
	return ev.Start.Format("15:04")
}

func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	if n < 1 {
		return ""
	}
	return string(runes[:n-1]) + "…"
}

// truncateToWidth returns s shortened (with a trailing "…") so it measures
// no wider than maxW pixels in dc's current font face. Caller must
// SetFontFace before invoking. Returns s unchanged if it already fits.
func truncateToWidth(dc *gg.Context, s string, maxW float64) string {
	if w, _ := dc.MeasureString(s); w <= maxW {
		return s
	}
	const ellipsis = "…"
	runes := []rune(s)
	for len(runes) > 1 {
		runes = runes[:len(runes)-1]
		candidate := strings.TrimRight(string(runes), " ") + ellipsis
		if w, _ := dc.MeasureString(candidate); w <= maxW {
			return candidate
		}
	}
	return ellipsis
}

func writePNG(w io.Writer, img image.Image) error {
	if err := png.Encode(w, img); err != nil {
		return fmt.Errorf("write png: %w", err)
	}
	return nil
}
