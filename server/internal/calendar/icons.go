package calendar

import "github.com/fogleman/gg"

// drawWifi renders a 4-bar wifi icon. signal is 0..4 (filled bars).
// Origin (x,y) is the top-left of the icon.
func drawWifi(dc *gg.Context, x, y float64, signal int) {
	const (
		bars            = 4
		barW            = 4.0
		gap             = 2.0
		baseH           = 18.0 // height of the tallest (rightmost) bar
		wifiBarBaseH    = 4.0
		wifiBarStep     = 4.0
	)
	dc.SetRGB(0, 0, 0)
	baseY := y + baseH
	for i := range bars {
		bx := x + float64(i)*(barW+gap)
		bh := wifiBarBaseH + float64(i)*wifiBarStep // 4, 8, 12, 16
		by := baseY - bh
		dc.DrawRectangle(bx, by, barW, bh)
		if i < signal {
			dc.Fill()
		} else {
			dc.SetLineWidth(1)
			dc.Stroke()
		}
	}
}

// drawBattery renders a battery icon with horizontal fill based on pct (0-100).
// Origin (x,y) is the top-left of the body (not including nub).
func drawBattery(dc *gg.Context, x, y float64, pct int) {
	const (
		bodyW      = 34.0
		bodyH      = 16.0
		nubW       = 3.0
		nubH       = 8.0
		padding    = 3.0
		strokeW    = 2.0
		pctMax     = 100
	)
	dc.SetRGB(0, 0, 0)
	dc.SetLineWidth(strokeW)
	dc.DrawRectangle(x, y, bodyW, bodyH)
	dc.Stroke()
	// nub
	dc.DrawRectangle(x+bodyW, y+(bodyH-nubH)/2, nubW, nubH)
	dc.Fill()
	// fill bar
	innerW := bodyW - (padding + padding)
	innerH := bodyH - (padding + padding)
	if pct < 0 {
		pct = 0
	}
	if pct > pctMax {
		pct = pctMax
	}
	fillW := innerW * float64(pct) / pctMax
	if fillW > 0 {
		dc.DrawRectangle(x+padding, y+padding, fillW, innerH)
		dc.Fill()
	}
}
