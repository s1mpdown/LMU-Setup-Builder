package main

import (
	_ "embed"
	"encoding/csv"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// trackmap.go — 2D track outlines and recorded-session loading for the
// REVIEW tab.
//
// Outlines come from two sources:
//   - learned: the first full lap of position data at a track is
//     captured by outlineLearner and stored as JSON under the app's
//     config dir (tracks/<name>.json), permanently.
//   - embedded: Ari's pre-v1.3 CSVs carry no position data, so the
//     Daytona centreline he traced (from his best-laps animation) is
//     embedded and REVIEW positions dots along it by lap distance.
//
// Everything here is platform-neutral and file paths are parameters,
// so it is unit-testable off Windows.

// Outline is a closed-loop centreline as raw points in an arbitrary
// 2D space (world metres for learned tracks, SVG units for the
// embedded Daytona). Renderers normalise by bounds.
type Outline struct {
	Track string       `json:"track"`
	Pts   [][2]float64 `json:"pts"`
}

// CumLengths returns the cumulative arc length at each point plus the
// total loop length — used to place a dot by lap-distance fraction.
func (o *Outline) CumLengths() ([]float64, float64) {
	n := len(o.Pts)
	if n == 0 {
		return nil, 0
	}
	cum := make([]float64, n+1)
	for i := 0; i < n; i++ {
		a := o.Pts[i]
		b := o.Pts[(i+1)%n]
		cum[i+1] = cum[i] + math.Hypot(b[0]-a[0], b[1]-a[1])
	}
	return cum, cum[n]
}

// PointAt returns the point at arc-length fraction f (0..1) of the loop.
func (o *Outline) PointAt(f float64) (float64, float64) {
	cum, total := o.CumLengths()
	if total <= 0 || len(o.Pts) == 0 {
		return 0, 0
	}
	target := f * total
	for target < 0 {
		target += total
	}
	for target >= total {
		target -= total
	}
	for i := 0; i < len(o.Pts); i++ {
		if cum[i+1] >= target {
			seg := cum[i+1] - cum[i]
			t := 0.0
			if seg > 0 {
				t = (target - cum[i]) / seg
			}
			a := o.Pts[i]
			b := o.Pts[(i+1)%len(o.Pts)]
			return a[0] + (b[0]-a[0])*t, a[1] + (b[1]-a[1])*t
		}
	}
	p := o.Pts[0]
	return p[0], p[1]
}

func outlinePath(dir, track string) string {
	name := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		}
		return '_'
	}, track)
	return filepath.Join(dir, name+".json")
}

func saveOutline(dir string, o *Outline) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	b, err := json.Marshal(o)
	if err != nil {
		return err
	}
	return os.WriteFile(outlinePath(dir, o.Track), b, 0644)
}

func loadOutline(dir, track string) (*Outline, error) {
	b, err := os.ReadFile(outlinePath(dir, track))
	if err != nil {
		return nil, err
	}
	var o Outline
	if err := json.Unmarshal(b, &o); err != nil {
		return nil, err
	}
	if len(o.Pts) < 10 {
		return nil, os.ErrInvalid
	}
	return &o, nil
}

//go:embed daytona_outline.json
var daytonaOutlineJSON []byte

// embeddedOutline returns a built-in outline by track name ("" if none).
func embeddedOutline(track string) *Outline {
	if !strings.Contains(strings.ToLower(track), "daytona") && track != "" {
		return nil
	}
	// The embedded file is a BARE [[x, y], ...] array (it was traced
	// for the best-laps animation), not an Outline object. Preview1
	// unmarshalled it into the struct, which fails on an array — so
	// this returned nil and the REVIEW map drew NOTHING for every old
	// position-less CSV. Parse the array form; accept the object form
	// too, for any future outline file.
	var pts [][2]float64
	if err := json.Unmarshal(daytonaOutlineJSON, &pts); err == nil && len(pts) >= 10 {
		return &Outline{Track: "Daytona", Pts: pts}
	}
	var o Outline
	if err := json.Unmarshal(daytonaOutlineJSON, &o); err != nil || len(o.Pts) < 10 {
		return nil
	}
	return &o
}

// outlineLearner captures the first full lap of position data at a
// track. Feed it every live sample; it returns the finished outline
// exactly once (when the lap after the captured one begins and the
// capture covered nearly the whole lap).
type outlineLearner struct {
	track   string
	started bool
	lap     int
	pts     [][2]float64
	lastX   float64
	lastZ   float64
	maxDist float64
	done    bool
}

func (l *outlineLearner) reset(track string) {
	l.track = track
	l.started = false
	l.pts = nil
	l.maxDist = 0
}

// Observe consumes one sample. Returns a completed outline (once) or nil.
func (l *outlineLearner) Observe(s Sample) *Outline {
	if l.done || !s.OK || (s.PosX == 0 && s.PosZ == 0) || s.InPits {
		return nil
	}
	if l.track == "" {
		l.track = s.TrackName
	}
	if s.TrackName != "" && l.track != "" && s.TrackName != l.track {
		l.reset(s.TrackName)
	}
	if !l.started {
		// Begin at a line crossing into a fresh lap.
		if s.LapDist > 300 {
			return nil
		}
		l.started = true
		l.lap = s.LapNumber
		l.pts = [][2]float64{{s.PosX, s.PosZ}}
		l.lastX, l.lastZ = s.PosX, s.PosZ
		l.maxDist = s.LapDist
		return nil
	}
	if s.LapNumber == l.lap {
		if s.LapDist > l.maxDist {
			l.maxDist = s.LapDist
		}
		if math.Hypot(s.PosX-l.lastX, s.PosZ-l.lastZ) >= 15 {
			l.pts = append(l.pts, [2]float64{s.PosX, s.PosZ})
			l.lastX, l.lastZ = s.PosX, s.PosZ
		}
		return nil
	}
	if s.LapNumber > l.lap {
		// The captured lap just ended. Keep it when it saw nearly the
		// whole lap (not an out-lap fragment or a pit-lane shortcut).
		full := s.TrackLength > 0 && l.maxDist > 0.92*s.TrackLength
		if full && len(l.pts) >= 40 {
			l.done = true
			return &Outline{Track: l.track, Pts: l.pts}
		}
		l.started = false
		l.pts = nil
		l.maxDist = 0
		return nil
	}
	// Lap counter went backwards (garage): start over next lap.
	l.started = false
	l.pts = nil
	l.maxDist = 0
	return nil
}

// ---- REVIEW session loading ----

// TeleSample is one row of a recorded CSV, old (10-column) or new
// (25-column) format. Pos fields exist only in the new format.
type TeleSample struct {
	ET     float64
	Lap    int
	Dist   float64
	Speed  float64
	Thr    float64
	Brk    float64
	Steer  float64
	Gear   int
	RPM    float64
	Fuel   float64
	X, Z   float64
	HasPos bool
}

// TeleLap is one lap's samples in time order.
type TeleLap struct {
	Number  int
	Samples []TeleSample
}

// LapTime is the lap's duration from its own timestamps (last − first).
func (l *TeleLap) LapTime() float64 {
	if len(l.Samples) < 2 {
		return 0
	}
	return l.Samples[len(l.Samples)-1].ET - l.Samples[0].ET
}

// LoadTeleSession parses a telemetry CSV (either column format) into
// laps. Laps with fewer than 50 samples are dropped (fragments).
func LoadTeleSession(path string) ([]TeleLap, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	r.ReuseRecord = true
	head, err := r.Read()
	if err != nil {
		return nil, err
	}
	col := map[string]int{}
	for i, h := range head {
		col[strings.TrimSpace(h)] = i
	}
	get := func(rec []string, name string) (float64, bool) {
		i, ok := col[name]
		if !ok || i >= len(rec) {
			return 0, false
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(rec[i]), 64)
		return v, err == nil
	}
	var laps []TeleLap
	byNum := map[int]*TeleLap{}
	for {
		rec, err := r.Read()
		if err != nil {
			break
		}
		et, ok1 := get(rec, "et_s")
		lapF, ok2 := get(rec, "lap")
		if !ok1 || !ok2 {
			continue
		}
		ts := TeleSample{ET: et, Lap: int(lapF)}
		ts.Dist, _ = get(rec, "lap_dist_m")
		ts.Speed, _ = get(rec, "speed_kmh")
		ts.Thr, _ = get(rec, "throttle")
		ts.Brk, _ = get(rec, "brake")
		ts.Steer, _ = get(rec, "steering")
		if g, ok := get(rec, "gear"); ok {
			ts.Gear = int(g)
		}
		ts.RPM, _ = get(rec, "rpm")
		ts.Fuel, _ = get(rec, "fuel_l")
		if x, ok := get(rec, "pos_x"); ok {
			if z, ok2 := get(rec, "pos_z"); ok2 {
				ts.X, ts.Z, ts.HasPos = x, z, true
			}
		}
		lp := byNum[ts.Lap]
		if lp == nil {
			laps = append(laps, TeleLap{Number: ts.Lap})
			lp = &laps[len(laps)-1]
			byNum[ts.Lap] = lp
		}
		lp.Samples = append(lp.Samples, ts)
	}
	out := laps[:0]
	for _, l := range laps {
		if len(l.Samples) >= 50 {
			out = append(out, l)
		}
	}
	return out, nil
}

// distCurve returns the lap's samples as a clean elapsed-vs-distance
// curve. A recorded lap opens with a few samples still carrying the
// OLD lap's end distance (~track length) before LapDist wraps to ~0
// — the same line-crossing skew the live trace seeding guards
// against. Scanning from raw sample 0 made every distance lookup
// match that stale head first, so etAtDist returned ~0 everywhere
// and the REVIEW delta curve rendered flat. The curve therefore
// starts at the wrap (the first sample under 300 m, when the lap
// opens with a stale head) and keeps only forward-moving samples.
func distCurve(l *TeleLap) []TeleSample {
	ss := l.Samples
	start := 0
	if len(ss) > 0 && ss[0].Dist > 300 {
		for i := range ss {
			if ss[i].Dist < 300 {
				start = i
				break
			}
		}
	}
	out := make([]TeleSample, 0, len(ss)-start)
	for _, s := range ss[start:] {
		if len(out) == 0 || s.Dist >= out[len(out)-1].Dist {
			out = append(out, s)
		}
	}
	return out
}

// LapDeltaAt computes lap A minus lap B elapsed time at distance d,
// by interpolating each lap's ET-versus-distance curve. Positive =
// A is slower than B at that point. ok=false when d is outside
// either lap's covered distance.
func LapDeltaAt(a, b *TeleLap, d float64) (float64, bool) {
	ea, ok1 := etAtDist(a, d)
	eb, ok2 := etAtDist(b, d)
	if !ok1 || !ok2 {
		return 0, false
	}
	return ea - eb, true
}

func etAtDist(l *TeleLap, d float64) (float64, bool) {
	ss := distCurve(l)
	if len(ss) < 2 || d < ss[0].Dist {
		return 0, false
	}
	// The curve is non-decreasing in distance; scan for the
	// bracketing pair.
	for i := 1; i < len(ss); i++ {
		if ss[i].Dist >= d {
			d0, d1 := ss[i-1].Dist, ss[i].Dist
			if d1 == d0 {
				return ss[i].ET - ss[0].ET, true
			}
			t := (d - d0) / (d1 - d0)
			return (ss[i-1].ET + (ss[i].ET-ss[i-1].ET)*t) - ss[0].ET, true
		}
	}
	return 0, false
}

// SampleAtDist returns the sample nearest distance d in a lap,
// searching the cleaned distance curve (never the stale head).
func SampleAtDist(l *TeleLap, d float64) *TeleSample {
	curve := distCurve(l)
	var best *TeleSample
	bd := math.MaxFloat64
	for i := range curve {
		if dd := math.Abs(curve[i].Dist - d); dd < bd {
			bd = dd
			best = &curve[i]
		}
	}
	return best
}

// SessionTrackLength estimates a recorded session's lap length: the
// largest lap distance any lap reached (recorded distances carry the
// line-crossing skew, so this lands within a few metres of truth).
func SessionTrackLength(laps []TeleLap) float64 {
	m := 0.0
	for _, l := range laps {
		for _, s := range l.Samples {
			if s.Dist > m {
				m = s.Dist
			}
		}
	}
	return m
}

// Full reports whether a recorded lap is a complete, honestly driven
// lap: its distance curve starts at the line, ends past 95% of the
// lap, and its samples cover at least 95% of the lap's 100 m bins.
// Sessions are full of fragments that fail this — laps that begin
// mid-lap, in-lap stubs, garage teleports, and reset-to-track laps
// whose endpoints span the lap while a whole sector was never driven
// (Ari's files contain a 90 s "lap" of a 108 s Daytona). Only full
// laps may be auto-picked or compared in REVIEW.
func (l *TeleLap) Full(trackLen float64) bool {
	if trackLen <= 0 || len(l.Samples) < 50 {
		return false
	}
	curve := distCurve(l)
	if len(curve) < 50 {
		return false
	}
	if curve[0].Dist > 300 || curve[len(curve)-1].Dist < 0.95*trackLen {
		return false
	}
	bins := int(math.Ceil(trackLen / 100))
	hit := make([]bool, bins)
	for _, s := range curve {
		if s.Dist < 0 {
			continue
		}
		b := int(s.Dist / 100)
		if b >= bins {
			b = bins - 1
		}
		hit[b] = true
	}
	n := 0
	for _, h := range hit {
		if h {
			n++
		}
	}
	return float64(n)/float64(bins) >= 0.95
}

// FullLaps returns the session's complete laps, in order. When no
// lap qualifies (a fragments-only recording) it returns every lap,
// so the REVIEW tab degrades instead of going empty.
func FullLaps(laps []TeleLap) []TeleLap {
	tl := SessionTrackLength(laps)
	var out []TeleLap
	for i := range laps {
		if laps[i].Full(tl) {
			out = append(out, laps[i])
		}
	}
	if len(out) == 0 {
		return laps
	}
	return out
}
