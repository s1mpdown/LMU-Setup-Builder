package main

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Overlay panel switches, shared between the app window (toggle
// switches) and the overlay renderer. Every panel is individually
// toggleable — off means zero drawing cost, which matters on a GPU
// Ari wants to protect.
//
// v1.3 panel set: the old Relative panel became the Timing Tower (it
// keeps the "relative" settings key and saved position, so his toggle
// and dragged spot carry over), and the standalone Fuel panel is gone
// (fuel lives in the Engineer panel now) — its key is simply no
// longer read or written.
var ovState struct {
	Shown    atomic.Bool
	Inputs   atomic.Bool
	Delta    atomic.Bool
	Tower    atomic.Bool
	Tyres    atomic.Bool
	Engineer atomic.Bool
	Flags    atomic.Bool // flag pill master switch (v1.3.0-preview3)
	Edit     atomic.Bool // MOVE mode: window takes mouse input for dragging
}

// Panel layout: user-dragged top-left positions (screen px) and, since
// v1.3, a free scale per panel (drag the bottom-right corner in MOVE
// mode; ~0.6x–2.0x; double-click the corner resets to designed size).
// A missing position means "use the default anchor"; a missing scale
// means the designed size.
var ovLayout struct {
	mu     sync.Mutex
	pos    map[string][2]int
	scales map[string]float64
	legacy float64 // pre-1.3 global scale, kept as the migration seed
}

func ovScaleVal() float64 {
	ovLayout.mu.Lock()
	defer ovLayout.mu.Unlock()
	if ovLayout.legacy <= 0 {
		return 1.0
	}
	return ovLayout.legacy
}

func ovSetScale(f float64) {
	ovLayout.mu.Lock()
	ovLayout.legacy = f
	ovLayout.mu.Unlock()
}

// deltaDisplay converts an internal delta to the value the panel
// shows. Internally DeltaAgainstBest (and the sector latching in
// histPush) speak "ahead-positive": positive means the current lap
// is AHEAD of the reference. The panel must read like LMU's own
// delta widget instead — LOSING time is a POSITIVE (red) number,
// GAINING is NEGATIVE (green) — so the display layer negates.
// Settled by Ari's field photo of preview2 (2026-11-02): for the
// same sector LMU showed +3.844 while our panel showed −3.84.
func deltaDisplay(d float64) float64 { return -d }

// flagsPanelOn resolves the flag pill's toggle from the saved panel
// map. The "flags" key arrived in v1.3.0-preview3, so configs saved
// by earlier builds don't carry it — an ABSENT key must mean ON
// (the pill's historical behaviour), unlike the other panels where
// a missing key reads as off.
func flagsPanelOn(p map[string]bool) bool {
	if v, ok := p["flags"]; ok {
		return v
	}
	return true
}

// ---- session-aware visibility (v1.3.0-preview4, freshness in preview10) ----
//
// Ari: "can i make the overlay only come after i joining session?
// auto. and auto closed after leaving?" Until now the overlay drew
// whenever the master toggle was on and the feed was live — and the
// feed is live in LMU's menus too (the shared-memory maps stay
// mapped), so panels and the flag pill floated over the menus, the
// garage UI, and even his desktop with the game in the background.
//
// sampleInSession decides whether THIS sample's VALUES show a
// joined session — the player in the car, in any in-session state
// (on track, in the pit box between runs, sitting in the garage
// screen of a loaded session). It is an OR of the signals the feed
// already proves in the field, weakest assumptions first:
//   - a player vehicle is present in the scoring field (in the
//     menus there is no field at all);
//   - GamePhase is in the active range 1..7 (header: 1 recon,
//     2 grid walk, 3 formation, 4 countdown, 5 green, 6 FCY,
//     7 session stopped; 0 = before the session, 8 = over,
//     9 = paused heartbeat — none of those three count);
//   - the session clock is running (SessionET > 0).
//
// Any one of them holding means a session is loaded. The gate
// below adds the race-safety hysteresis on top.
//
// Preview10: values alone are NOT enough to gate on. When LMU
// leaves a session and returns to the menus, the shared-memory
// maps stay mapped with the last session's contents FROZEN in
// place — the field still listed, the phase still in range,
// SessionET still > 0 — and the parse loop keeps succeeding on
// those leftovers, so a values-only gate reads "in session"
// forever: the overlay follows him into the menus and never
// leaves (field report 2026-11-02: a whole day of "session
// joined" log lines and not one "session left"). sessionFeed
// below therefore only lets the STRONG values through (the phase
// and player legs — sessionSustain) while the feed proves itself
// alive: the session clock must have ADVANCED within the last
// few seconds. Frozen data counts as absent, and the gate's
// absence timer then hides the overlay a bounded time after the
// session truly ends. The bare clock leg no longer sustains the
// joined state either: a leftover ET > 0 with no player and no
// active phase is menu residue, not a session.
func sampleInSession(s Sample) bool {
	return sessionSustain(s) || (s.OK && s.SessionET > 0)
}

// sessionSustain reports the strong in-session signals: an
// active GamePhase (1..7) or the player vehicle present in the
// scoring field. The session clock alone is deliberately not one
// of them — see the preview10 note above.
func sessionSustain(s Sample) bool {
	if !s.OK {
		return false
	}
	if s.GamePhase >= 1 && s.GamePhase <= 7 {
		return true
	}
	for _, c := range s.Field {
		if c.IsPlayer {
			return true
		}
	}
	return false
}

// sessionFreshWindow is how long the session clock may go
// without advancing before the feed counts as frozen. A live
// session clock advances continuously — on track and on the
// garage screen of a loaded session alike — so 3 s sits far
// above the feed's update cadence and far below anything a
// driver could notice at a transition.
const sessionFreshWindow = 3 * time.Second

// sessionFeed folds raw samples into the gate's in-session
// reading: strong values (sessionSustain) AND a live clock. It
// tracks SessionET across calls; the clock counts as live only
// once it has actually CHANGED value at least once (a first
// sighting proves nothing — frozen menu leftovers carry
// plausible values) and has changed within sessionFreshWindow.
// Frozen samples return false, feeding the gate's absence timer.
// Touched only by the overlay loop.
type sessionFeed struct {
	lastET  float64
	haveET  bool      // a valid sample's clock has been recorded
	moved   bool      // the clock has changed value at least once
	movedAt time.Time // last wall time the clock was seen to change
}

func (f *sessionFeed) update(s Sample, now time.Time) bool {
	if s.OK {
		if !f.haveET {
			f.lastET, f.haveET = s.SessionET, true
		} else if s.SessionET != f.lastET {
			f.lastET = s.SessionET
			f.moved = true
			f.movedAt = now
		}
	}
	if !f.moved || now.Sub(f.movedAt) > sessionFreshWindow {
		return false
	}
	return sessionSustain(s)
}

// ovSessionFeed is the overlay loop's freshness tracker instance.
var ovSessionFeed sessionFeed

// Hysteresis for the session gate: appear promptly on join, but
// take a long, continuous absence before hiding. Loading screens,
// session transitions and brief feed dropouts must never blank the
// overlay anywhere near driving; mid-race the in-session signals
// are continuous by construction, so the hide delay only ever
// elapses when he has genuinely left.
const (
	sessionShowDelay = 1 * time.Second
	sessionHideDelay = 10 * time.Second
)

// sessionGate folds raw in-session readings into overlay
// visibility. update returns (visible, joined, left); joined/left
// are true only on the call where visibility flips, so the caller
// can log each transition exactly once. Touched only by the
// overlay loop.
type sessionGate struct {
	visible  bool
	inSince  time.Time // start of the current in-session streak
	outSince time.Time // start of the current out-of-session streak
}

func (g *sessionGate) update(in bool, now time.Time) (visible, joined, left bool) {
	if in {
		g.outSince = time.Time{}
		if g.inSince.IsZero() {
			g.inSince = now
		}
		if !g.visible && now.Sub(g.inSince) >= sessionShowDelay {
			g.visible = true
			joined = true
		}
	} else {
		g.inSince = time.Time{}
		if g.visible {
			if g.outSince.IsZero() {
				g.outSince = now
			}
			if now.Sub(g.outSince) >= sessionHideDelay {
				g.visible = false
				left = true
			}
		}
	}
	return g.visible, joined, left
}

// ovSessionGate is the overlay loop's visibility gate instance.
var ovSessionGate sessionGate

// sessionLabel names the session type from the scoring header's
// mSession value. The rF2/LMU header documents RANGES, not single
// values: 0 = test day, 1-4 = practice, 5-8 = qualifying, 9 = warmup,
// 10-13 = race. Anything outside the documented ranges is a plain
// SESSION (never a guessed name).
func sessionLabel(v int) string {
	switch {
	case v == 0:
		return "TEST DAY"
	case v >= 1 && v <= 4:
		return "PRACTICE"
	case v >= 5 && v <= 8:
		return "QUALIFYING"
	case v == 9:
		return "WARMUP"
	case v >= 10 && v <= 13:
		return "RACE"
	default:
		return "SESSION"
	}
}

// fmtTimeLeft renders a seconds-remaining count the way a session
// clock reads: M:SS under an hour, H:MM:SS at or above it. Partial
// seconds floor away (a countdown shows whole seconds it still has).
func fmtTimeLeft(sec float64) string {
	t := int(sec)
	if t < 0 {
		t = 0
	}
	if t >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", t/3600, (t%3600)/60, t%60)
	}
	return fmt.Sprintf("%d:%02d", t/60, t%60)
}

// truncateTrack caps a track name at max runes, ending the cut with
// an ellipsis so the tower header never overflows its panel.
func truncateTrack(name string, max int) string {
	name = strings.TrimSpace(name)
	r := []rune(name)
	if len(r) <= max {
		return name
	}
	if max < 2 {
		return string(r[:max])
	}
	return string(r[:max-1]) + "…"
}

// towerContextLines builds the tower header's session-context block
// (preview6, Ari's ask: the tower showed no time left, session type
// or event identity — the standard feed publishes no event name, so
// line 1 is the TRACK name, the venue identity the feed does carry):
//
//	line 1: track name (only when the feed names one)
//	line 2: SESSION LABEL, plus " · M:SS LEFT" in a timed session
//
// A lap-counted session has no valid end time (SessionLeft <= 0), so
// line 2 is the label alone — never a bogus 0:00.
func towerContextLines(s Sample) []string {
	var lines []string
	if name := truncateTrack(s.TrackName, 60); name != "" {
		lines = append(lines, name)
	}
	line := sessionLabel(s.Session)
	if s.SessionLeft > 0 {
		line += " · " + fmtTimeLeft(s.SessionLeft) + " LEFT"
	}
	return append(lines, line)
}

// rpmZone classifies one segment of the Inputs panel's RPM bar by
// its position in the bar (sf = segment fraction 0..1): 0 = teal
// (the working range, ~first 55%), 1 = orange (~next 30%), 2 = red
// (the shift zone, ~last 15%). Preview1's thresholds (orange above
// 88%, red above 97%) left red as ONE segment of 28 and nearly
// everything teal/green — Ari's field verdict. Kept platform-
// neutral so the zoning is unit-testable off Windows.
func rpmZone(sf float64) int {
	switch {
	case sf > 0.85:
		return 2
	case sf > 0.55:
		return 1
	default:
		return 0
	}
}

// rpmLimiterFlash reports which colour phase the Inputs RPM bar is
// in while the pit limiter is engaged. ms is a Unix-milli timestamp
// — the same time source as the overlay's existing blink logic.
// Each phase lasts ~167 ms, so the lit bar cycles limiter-blue /
// paper-white at ~3 Hz while the limiter holds the revs (Ari's
// preview5 request: "rpm while on pit limiter flashing and
// changing color"). true = the blue phase, false = the white
// phase. Kept platform-neutral so the phase is unit-testable.
func rpmLimiterFlash(ms int64) bool {
	return (ms/167)%2 == 0
}

// ovPanelScale returns one panel's free scale (1 = designed size).
func ovPanelScale(key string) float64 {
	ovLayout.mu.Lock()
	defer ovLayout.mu.Unlock()
	if v, ok := ovLayout.scales[key]; ok && v >= 0.6 && v <= 2.0 {
		return v
	}
	if ovLayout.legacy > 0 {
		return ovLayout.legacy
	}
	return 1
}

func ovSetPanelScale(key string, f float64) {
	if f < 0.6 {
		f = 0.6
	}
	if f > 2.0 {
		f = 2.0
	}
	ovLayout.mu.Lock()
	if ovLayout.scales == nil {
		ovLayout.scales = map[string]float64{}
	}
	ovLayout.scales[key] = f
	ovLayout.mu.Unlock()
}

// ovPanelPos returns the panel's top-left: the user's dragged spot when one
// is saved, otherwise the supplied default anchor.
func ovPanelPos(key string, defX, defY int) (int, int) {
	ovLayout.mu.Lock()
	defer ovLayout.mu.Unlock()
	if p, ok := ovLayout.pos[key]; ok {
		return p[0], p[1]
	}
	return defX, defY
}

func ovSetPanelPos(key string, x, y int) {
	ovLayout.mu.Lock()
	if ovLayout.pos == nil {
		ovLayout.pos = map[string][2]int{}
	}
	ovLayout.pos[key] = [2]int{x, y}
	ovLayout.mu.Unlock()
}

// ovAllPositions snapshots the saved positions (for display clamping).
func ovAllPositions() map[string][2]int {
	ovLayout.mu.Lock()
	defer ovLayout.mu.Unlock()
	out := map[string][2]int{}
	for k, v := range ovLayout.pos {
		out[k] = v
	}
	return out
}

// overlayInit applies saved preferences (or sensible defaults) at startup.
// Done once in main() before the overlay loop starts, so no sync needed.
func overlayInit(on bool, p map[string]bool, pos map[string][]int, scales map[string]float64, legacyScale float64) {
	// The overlay arms ITSELF at startup (v1.3.0-preview8, Ari's
	// ask: "i still need to hit show / hide overlay tho that thing
	// not auto"). The session gate alone decides when the panels
	// are actually on screen, so no button press is ever required:
	// the persisted `on` value is deliberately NOT honoured here.
	// The config's shown key is still read (and still written by
	// overlayConfig) for compatibility, but a stored false — saved
	// by an older version, or left behind by a manual HIDE — must
	// not disarm startup. SHOW/HIDE is a per-run override only: it
	// flips ovState.Shown for the current run, and the next launch
	// re-arms here regardless of what was saved.
	ovState.Shown.Store(true)
	ovLayout.mu.Lock()
	ovLayout.legacy = legacyScale
	ovLayout.pos = map[string][2]int{}
	for k, v := range pos {
		if len(v) == 2 {
			ovLayout.pos[k] = [2]int{v[0], v[1]}
		}
	}
	ovLayout.scales = map[string]float64{}
	for k, v := range scales {
		if v >= 0.6 && v <= 2.0 {
			ovLayout.scales[k] = v
		}
	}
	ovLayout.mu.Unlock()
	if p == nil {
		ovState.Inputs.Store(true)
		ovState.Delta.Store(true)
		ovState.Tower.Store(true)
		ovState.Tyres.Store(true)
		ovState.Engineer.Store(true)
		ovState.Flags.Store(true)
		return
	}
	ovState.Inputs.Store(p["inputs"])
	ovState.Delta.Store(p["delta"])
	ovState.Tower.Store(p["relative"]) // tower inherits the relative toggle
	ovState.Tyres.Store(p["tyres"])
	ovState.Engineer.Store(p["engineer"])
	ovState.Flags.Store(flagsPanelOn(p)) // absent key = on (pre-preview3 configs)
}

func overlayConfig() (bool, map[string]bool, map[string][]int, map[string]float64, float64) {
	ovLayout.mu.Lock()
	pos := map[string][]int{}
	for k, v := range ovLayout.pos {
		pos[k] = []int{v[0], v[1]}
	}
	scales := map[string]float64{}
	for k, v := range ovLayout.scales {
		scales[k] = v
	}
	legacy := ovLayout.legacy
	ovLayout.mu.Unlock()
	return ovState.Shown.Load(), map[string]bool{
		"inputs":   ovState.Inputs.Load(),
		"delta":    ovState.Delta.Load(),
		"relative": ovState.Tower.Load(),
		"tyres":    ovState.Tyres.Load(),
		"engineer": ovState.Engineer.Load(),
		"flags":    ovState.Flags.Load(),
	}, pos, scales, legacy
}
