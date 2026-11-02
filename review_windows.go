//go:build windows

package main

// review_windows.go — the REVIEW tab: pick a recorded session and two
// laps, then watch them race on the 2D map with the delta curve and
// pedal traces following the same cursor. Sessions with position data
// (recorded by v1.3+) draw their real driven lines; older CSVs (all
// Daytona) ride the embedded Daytona outline by lap distance.

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"
)

var rev struct {
	sessionCB *walk.ComboBox
	lapACB    *walk.ComboBox
	lapBCB    *walk.ComboBox
	canvas    *walk.CustomWidget
	slider    *walk.Slider
	playBtn   *walk.PushButton
	infoLbl   *walk.Label

	files   []string // full paths, newest first
	laps    []TeleLap
	lapA    *TeleLap
	lapB    *TeleLap
	frac    float64 // cursor: 0..1 of the compared distance
	playing bool
	speed   float64
}

var revSpeeds = []float64{0.5, 1, 2, 4}

func reviewPage() TabPage {
	return TabPage{
		Title:  "REVIEW",
		Layout: VBox{Margins: Margins{Left: 12, Top: 10, Right: 12, Bottom: 10}, Spacing: 6},
		Children: []Widget{
			Composite{
				Layout: HBox{MarginsZero: true, Spacing: 8},
				Children: []Widget{
					Label{Text: "Session", TextColor: walk.RGB(0xa8, 0xbc, 0xd6)},
					ComboBox{AssignTo: &rev.sessionCB, MinSize: Size{Width: 300}, OnCurrentIndexChanged: revSessionPicked},
					PushButton{Text: "REFRESH", OnClicked: revRefreshSessions},
					HSpacer{},
					Label{Text: "Lap A (teal)", TextColor: walk.RGB(0x2e, 0xc4, 0xb6)},
					ComboBox{AssignTo: &rev.lapACB, MinSize: Size{Width: 150}, OnCurrentIndexChanged: revLapPicked},
					Label{Text: "Lap B (orange)", TextColor: walk.RGB(0xff, 0x9f, 0x1c)},
					ComboBox{AssignTo: &rev.lapBCB, MinSize: Size{Width: 150}, OnCurrentIndexChanged: revLapPicked},
				},
			},
			CustomWidget{
				AssignTo: &rev.canvas,
				MinSize:  Size{Width: 900, Height: 400},
				// Preview4: PaintBuffered renders the whole scene
				// (map + delta + pedals live in this one widget) to
				// an offscreen bitmap and blits it once per frame,
				// with the background erase suppressed. Playback at
				// 30 fps used to repaint unbuffered straight to the
				// screen — visible flicker on every play press.
				PaintMode: PaintBuffered,
				Paint:     revPaint,
				OnMouseDown: func(x, y int, button walk.MouseButton) {
					if button == walk.LeftButton {
						revCanvasClick(x, y)
					}
				},
				OnKeyDown: func(key walk.Key) {
					switch key {
					case walk.KeySpace:
						revTogglePlay()
					case walk.KeyLeft:
						revStep(-1)
					case walk.KeyRight:
						revStep(1)
					}
				},
			},
			Slider{
				AssignTo:       &rev.slider,
				MinValue:       0,
				MaxValue:       1000,
				OnValueChanged: revScrubbed,
			},
			Composite{
				Layout: HBox{MarginsZero: true, Spacing: 6},
				Children: []Widget{
					PushButton{AssignTo: &rev.playBtn, Text: "▶ PLAY", OnClicked: revTogglePlay},
					PushButton{Text: "◂ STEP", OnClicked: func() { revStep(-1) }},
					PushButton{Text: "STEP ▸", OnClicked: func() { revStep(1) }},
					PushButton{Text: "0.5×", OnClicked: func() { rev.speed = revSpeeds[0] }},
					PushButton{Text: "1×", OnClicked: func() { rev.speed = revSpeeds[1] }},
					PushButton{Text: "2×", OnClicked: func() { rev.speed = revSpeeds[2] }},
					PushButton{Text: "4×", OnClicked: func() { rev.speed = revSpeeds[3] }},
					HSpacer{},
					Label{AssignTo: &rev.infoLbl, Text: "Pick a session to review.", TextColor: walk.RGB(0xf7, 0xf7, 0xff), Font: Font{Family: "Consolas", PointSize: 10}},
				},
			},
		},
	}
}

func revMaxDist() float64 {
	m := 0.0
	for _, l := range []*TeleLap{rev.lapA, rev.lapB} {
		if l == nil {
			continue
		}
		for _, s := range distCurve(l) {
			if s.Dist > m {
				m = s.Dist
			}
		}
	}
	return m
}

func revRefreshSessions() {
	if rev.sessionCB == nil {
		return
	}
	entries, err := os.ReadDir(teleDir())
	rev.files = rev.files[:0]
	var names []string
	if err == nil {
		for _, e := range entries {
			n := e.Name()
			if strings.HasPrefix(n, "telemetry-") && strings.HasSuffix(n, ".csv") {
				rev.files = append(rev.files, filepath.Join(teleDir(), n))
			}
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(rev.files)))
	for _, f := range rev.files {
		names = append(names, filepath.Base(f))
	}
	if len(names) == 0 {
		names = []string{"(no recordings yet)"}
	}
	rev.sessionCB.SetModel(names)
	if len(rev.files) > 0 {
		rev.sessionCB.SetCurrentIndex(0)
		revSessionPicked()
	}
}

func revSessionPicked() {
	i := rev.sessionCB.CurrentIndex()
	if i < 0 || i >= len(rev.files) {
		return
	}
	laps, err := LoadTeleSession(rev.files[i])
	if err != nil || len(laps) == 0 {
		rev.infoLbl.SetText("Could not read that session.")
		return
	}
	// Only complete laps are pickable: sessions carry fragments
	// (mid-lap joins, in-lap stubs, teleport laps) whose raw times
	// can beat every real lap — preview1 auto-picked a 1:30 "lap"
	// of a 1:48 Daytona as the session's fastest.
	rev.laps = FullLaps(laps)
	laps = rev.laps
	model := make([]string, len(laps))
	best, second := -1, -1
	for i, l := range laps {
		model[i] = fmt.Sprintf("LAP %d · %s", l.Number, fmtLapT(l.LapTime()))
		if t := l.LapTime(); t > 20 {
			if best < 0 || t < laps[best].LapTime() {
				second, best = best, i
			} else if second < 0 || t < laps[second].LapTime() {
				second = i
			}
		}
	}
	rev.lapACB.SetModel(model)
	rev.lapBCB.SetModel(model)
	a, b := 0, 0
	if best >= 0 {
		a = best
	}
	if second >= 0 {
		b = second
	} else if len(laps) > 1 {
		b = 1
	}
	rev.lapACB.SetCurrentIndex(a)
	rev.lapBCB.SetCurrentIndex(b)
	rev.frac = 0
	revLapPicked()
}

func revLapPicked() {
	if rev.laps == nil {
		return
	}
	if i := rev.lapACB.CurrentIndex(); i >= 0 && i < len(rev.laps) {
		rev.lapA = &rev.laps[i]
	}
	if i := rev.lapBCB.CurrentIndex(); i >= 0 && i < len(rev.laps) {
		rev.lapB = &rev.laps[i]
	}
	revRepaint()
}

func revTogglePlay() {
	rev.playing = !rev.playing
	if rev.playBtn != nil {
		if rev.playing {
			rev.playBtn.SetText("❚❚ PAUSE")
		} else {
			rev.playBtn.SetText("▶ PLAY")
		}
	}
}

func revStep(dir int) {
	md := revMaxDist()
	if md <= 0 || rev.lapA == nil {
		return
	}
	// One step ≈ one second of lap A.
	d1s := md
	if t := rev.lapA.LapTime(); t > 0 {
		d1s = md / t * float64(dir)
	} else {
		d1s = md / 100 * float64(dir)
	}
	rev.frac += d1s / md
	revClampFrac()
	revSyncUI()
}

func revClampFrac() {
	if rev.frac < 0 {
		rev.frac = 0
	}
	if rev.frac > 1 {
		rev.frac = 1
	}
}

func revScrubbed() {
	if rev.slider == nil {
		return
	}
	rev.frac = float64(rev.slider.Value()) / 1000
	revRepaint()
	revUpdateInfo()
}

func revSyncUI() {
	if rev.slider != nil {
		rev.slider.SetValue(int(rev.frac * 1000))
	}
	revRepaint()
	revUpdateInfo()
}

func revRepaint() {
	if rev.canvas != nil {
		rev.canvas.Invalidate()
	}
}

// reviewTicker advances playback; started once from main().
func reviewTicker() {
	var last time.Time
	for {
		time.Sleep(33 * time.Millisecond)
		if !rev.playing || mw == nil {
			last = time.Time{}
			continue
		}
		now := time.Now()
		if last.IsZero() {
			last = now
			continue
		}
		dt := now.Sub(last).Seconds()
		last = now
		mw.Synchronize(func() {
			if rev.lapA == nil {
				return
			}
			t := rev.lapA.LapTime()
			if t <= 0 {
				return
			}
			rev.frac += dt * rev.speed / t
			if rev.frac >= 1 {
				rev.frac = 1
				revTogglePlay()
			}
			revSyncUI()
		})
	}
}

// ---- painting ----

var (
	revColBG      = walk.RGB(0x0d, 0x13, 0x21)
	revColPanel   = walk.RGB(0x11, 0x1d, 0x33)
	revColBorder  = walk.RGB(0x2f, 0x44, 0x66)
	revColPaper   = walk.RGB(0xf7, 0xf7, 0xff)
	revColMuted   = walk.RGB(0xa8, 0xbc, 0xd6)
	revColTeal    = walk.RGB(0x2e, 0xc4, 0xb6)
	revColOrange  = walk.RGB(0xff, 0x9f, 0x1c)
	revColBrake   = walk.RGB(0xff, 0x33, 0x55)
	revColOutline = walk.RGB(0x3d, 0x5a, 0x80)
)

// Layout rectangles (recomputed per paint from the canvas bounds).
// Preview7: the plots are a full-width vertical stack — map on
// top (dominant), delta in the middle, pedal traces at the
// bottom — filling the whole canvas instead of the old fixed
// column + strips anchored top-left. The split math lives in
// review_layout.go (pure, unit-tested); these only convert.
func revStack(b walk.Rectangle) (mapR, deltaR, traceR walk.Rectangle) {
	m, d, t := revStackRects(b.Width, b.Height)
	conv := func(r revRect) walk.Rectangle {
		return walk.Rectangle{X: b.X + r.X, Y: b.Y + r.Y, Width: r.W, Height: r.H}
	}
	return conv(m), conv(d), conv(t)
}
func revDeltaRect(b walk.Rectangle) walk.Rectangle {
	_, d, _ := revStack(b)
	return d
}
func revTraceRect(b walk.Rectangle) walk.Rectangle {
	_, _, t := revStack(b)
	return t
}
func revMapRect(b walk.Rectangle) walk.Rectangle {
	m, _, _ := revStack(b)
	return m
}

func revCanvasClick(x, y int) {
	if rev.canvas == nil || rev.lapA == nil || rev.lapB == nil {
		return
	}
	b := rev.canvas.Bounds()
	dr := revDeltaRect(b)
	if x >= dr.X && x < dr.X+dr.Width && y >= dr.Y && y < dr.Y+dr.Height {
		rev.frac = float64(x-dr.X) / float64(dr.Width)
		revClampFrac()
		revSyncUI()
	}
}

func revBrush(c walk.Color) *walk.SolidColorBrush {
	b, _ := walk.NewSolidColorBrush(c)
	return b
}

func revPen(c walk.Color, w int) *walk.CosmeticPen {
	p, _ := walk.NewCosmeticPen(walk.PenSolid, c)
	return p
}

func revPaint(cv *walk.Canvas, b walk.Rectangle) error {
	bg := revBrush(revColBG)
	defer bg.Dispose()
	_ = cv.FillRectangle(bg, b)
	if rev.lapA == nil {
		f, _ := walk.NewFont("Segoe UI", 11, 0)
		defer f.Dispose()
		return cv.DrawText("Record a session (the logger auto-records on track), then pick it above.", f, revColMuted, b, 0)
	}
	md := revMaxDist()
	curD := rev.frac * md

	// ---- map ----
	mr := revMapRect(b)
	panel := revBrush(revColPanel)
	defer panel.Dispose()
	_ = cv.FillRectangle(panel, mr)
	revDrawMap(cv, mr, curD)

	// ---- delta curve ----
	dr := revDeltaRect(b)
	_ = cv.FillRectangle(panel, dr)
	f10, _ := walk.NewFont("Consolas", 9, 0)
	defer f10.Dispose()
	_ = cv.DrawText("DELTA  A−B (s) — click to jump", f10, revColMuted, walk.Rectangle{X: dr.X + 6, Y: dr.Y + 4, Width: dr.Width - 12, Height: 14}, 0)
	if rev.lapB != nil {
		midY := dr.Y + dr.Height/2 + 8
		zero := revPen(revColBorder, 1)
		defer zero.Dispose()
		_ = cv.DrawLine(zero, walk.Point{X: dr.X + 4, Y: midY}, walk.Point{X: dr.X + dr.Width - 4, Y: midY})
		pen := revPen(revColOrange, 2)
		defer pen.Dispose()
		var prev *walk.Point
		steps := 220
		for i := 0; i <= steps; i++ {
			d := float64(i) / float64(steps) * md
			dv, ok := LapDeltaAt(rev.lapA, rev.lapB, d)
			if !ok {
				prev = nil
				continue
			}
			if dv > 3 {
				dv = 3
			}
			if dv < -3 {
				dv = -3
			}
			px := dr.X + 4 + int(float64(dr.Width-8)*float64(i)/float64(steps))
			py := midY - int(dv/3*float64(dr.Height/2-16))
			pt := walk.Point{X: px, Y: py}
			if prev != nil {
				_ = cv.DrawLine(pen, *prev, pt)
			}
			cp := pt
			prev = &cp
		}
		// cursor line
		cx := dr.X + 4 + int(float64(dr.Width-8)*rev.frac)
		cur := revPen(revColPaper, 1)
		defer cur.Dispose()
		_ = cv.DrawLine(cur, walk.Point{X: cx, Y: dr.Y + 4}, walk.Point{X: cx, Y: dr.Y + dr.Height - 4})
	}

	// ---- pedal traces ----
	tr := revTraceRect(b)
	_ = cv.FillRectangle(panel, tr)
	_ = cv.DrawText("PEDALS  throttle (teal) · brake (red) — lap A", f10, revColMuted, walk.Rectangle{X: tr.X + 6, Y: tr.Y + 4, Width: tr.Width - 12, Height: 14}, 0)
	revDrawTrace(cv, tr, curD)
	revUpdateInfo()
	return nil
}

// revDrawMap draws the outline (or driven paths), the driven lines up
// to the cursor, and the two racing dots.
func revDrawMap(cv *walk.Canvas, mr walk.Rectangle, curD float64) {
	type pt struct{ x, y float64 }
	var outline []pt
	usePos := rev.lapA != nil && len(rev.lapA.Samples) > 0 && rev.lapA.Samples[0].HasPos
	if usePos {
		for _, s := range rev.lapA.Samples {
			outline = append(outline, pt{s.X, s.Z})
		}
	} else if o := embeddedOutline("Daytona"); o != nil {
		for _, p := range o.Pts {
			outline = append(outline, pt{p[0], p[1]})
		}
	}
	if len(outline) < 2 {
		return
	}
	minX, maxX, minY, maxY := math.MaxFloat64, -math.MaxFloat64, math.MaxFloat64, -math.MaxFloat64
	for _, p := range outline {
		minX, maxX = math.Min(minX, p.x), math.Max(maxX, p.x)
		minY, maxY = math.Min(minY, p.y), math.Max(maxY, p.y)
	}
	pad := 14.0
	sx := (float64(mr.Width) - 2*pad) / (maxX - minX)
	sy := (float64(mr.Height) - 2*pad) / (maxY - minY)
	sc := math.Min(sx, sy)
	// Fit-and-centre: the uniform scale preserves the track's
	// aspect; the leftover slack in the roomier dimension is
	// split evenly on both sides so the outline sits centred
	// in its (now much wider) section instead of hugging the
	// top-left the way it did in the old narrow column.
	offX := pad + (float64(mr.Width)-2*pad-(maxX-minX)*sc)/2
	offY := pad + (float64(mr.Height)-2*pad-(maxY-minY)*sc)/2
	mapPt := func(x, y float64) walk.Point {
		return walk.Point{
			X: mr.X + int(offX+(x-minX)*sc),
			Y: mr.Y + int(offY+(maxY-y)*sc), // flip: north up
		}
	}
	// outline loop
	pen := revPen(revColOutline, 2)
	defer pen.Dispose()
	for i := range outline {
		a := mapPt(outline[i].x, outline[i].y)
		n := outline[(i+1)%len(outline)]
		_ = cv.DrawLine(pen, a, mapPt(n.x, n.y))
	}
	// driven lines + dots
	plot := func(l *TeleLap, col walk.Color, dotR int) {
		if l == nil {
			return
		}
		p2 := revPen(col, 2)
		defer p2.Dispose()
		var prev *walk.Point
		var dot *walk.Point
		if usePos {
			// Iterate the cleaned curve: the raw samples open with a
			// stale-head sample at ~track length, which would trip
			// the curD break immediately and draw nothing.
			for _, s := range distCurve(l) {
				if s.Dist > curD {
					break
				}
				p := mapPt(s.X, s.Z)
				if prev != nil {
					_ = cv.DrawLine(p2, *prev, p)
				}
				cp := p
				prev = &cp
				dot = &cp
			}
		} else if o := embeddedOutline("Daytona"); o != nil {
			md := revMaxDist()
			if md > 0 {
				steps := 120
				for i := 0; i <= steps; i++ {
					d := float64(i) / float64(steps) * curD
					ox, oy := o.PointAt(d / md)
					p := mapPt(ox, oy)
					if prev != nil {
						_ = cv.DrawLine(p2, *prev, p)
					}
					cp := p
					prev = &cp
					dot = &cp
				}
			}
		}
		if dot != nil {
			br := revBrush(col)
			defer br.Dispose()
			r := walk.Rectangle{X: dot.X - dotR, Y: dot.Y - dotR, Width: dotR * 2, Height: dotR * 2}
			_ = cv.FillEllipse(br, r)
		}
	}
	plot(rev.lapB, revColOrange, 5)
	plot(rev.lapA, revColTeal, 6)
}

func revDrawTrace(cv *walk.Canvas, tr walk.Rectangle, curD float64) {
	l := rev.lapA
	if l == nil || len(l.Samples) < 2 {
		return
	}
	md := revMaxDist()
	if md <= 0 {
		return
	}
	x0, y0 := tr.X+6, tr.Y+22
	w, h := tr.Width-12, tr.Height-30
	draw := func(get func(*TeleSample) float64, col walk.Color) {
		pen := revPen(col, 1)
		defer pen.Dispose()
		var prev *walk.Point
		for _, s := range distCurve(l) {
			px := x0 + int(s.Dist/md*float64(w))
			v := get(&s)
			if v < 0 {
				v = 0
			}
			if v > 1 {
				v = 1
			}
			py := y0 + int((1-v)*float64(h))
			p := walk.Point{X: px, Y: py}
			if prev != nil {
				_ = cv.DrawLine(pen, *prev, p)
			}
			cp := p
			prev = &cp
		}
	}
	draw(func(s *TeleSample) float64 { return s.Thr }, revColTeal)
	draw(func(s *TeleSample) float64 { return s.Brk }, revColBrake)
	cx := x0 + int(rev.frac*float64(w))
	cur := revPen(revColPaper, 1)
	defer cur.Dispose()
	_ = cv.DrawLine(cur, walk.Point{X: cx, Y: y0}, walk.Point{X: cx, Y: y0 + h})
}

func revUpdateInfo() {
	if rev.infoLbl == nil || rev.lapA == nil {
		return
	}
	md := revMaxDist()
	d := rev.frac * md
	sa := SampleAtDist(rev.lapA, d)
	txt := ""
	if sa != nil {
		txt = fmt.Sprintf("A LAP %d · %.0f m · %.0f km/h · G%d · %.0f rpm", rev.lapA.Number, d, sa.Speed, sa.Gear, sa.RPM)
	}
	if rev.lapB != nil {
		if sb := SampleAtDist(rev.lapB, d); sb != nil {
			txt += fmt.Sprintf("   |   B LAP %d · %.0f km/h", rev.lapB.Number, sb.Speed)
		}
		if dv, ok := LapDeltaAt(rev.lapA, rev.lapB, d); ok {
			txt += fmt.Sprintf("   |   Δ %+.2f s", dv)
		}
	}
	rev.infoLbl.SetText(txt)
}
