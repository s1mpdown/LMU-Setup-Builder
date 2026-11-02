package main

// preview3_test.go — regression tests for the v1.3.0-preview3 round:
//  1. delta sign in LMU's own convention at the display layer
//     (deltaDisplay; the lap-level cases live in fixround_test.go
//     and v13_test.go).
//  2. the Flags panel toggle: new "flags" settings key, default ON
//     when absent (pre-preview3 configs), pill gated by it.
//  3. flag transition logging (LogLine): one line per displayed-state
//     change, with the winning source attributed.
//  4. the hazard speed gate's moving-car case lives in
//     fixround_test.go; here: the BLUE leg proven on tower-proven
//     class data (no reliance on the unverified blue byte), its
//     inverse cases, and the pill priority hazard > blue.
//     (Preview4 rework: the game sector yellow is no longer a pill
//     source at all — see TestFlagGameSectorYellowIgnored.)

import (
	"strings"
	"testing"
)

// ---- 1. Delta display conversion ----

func TestDeltaDisplayGameConvention(t *testing.T) {
	// Internal +4 = 4 s AHEAD of the reference (gaining): the game
	// shows a negative (green) number.
	if got := deltaDisplay(4); got != -4 {
		t.Errorf("deltaDisplay(+4) = %v, want -4 (gaining reads negative)", got)
	}
	// The field photo: internal -3.84 (losing) must display +3.84,
	// matching LMU's +3.844 for the same sector.
	if got := deltaDisplay(-3.84); got != 3.84 {
		t.Errorf("deltaDisplay(-3.84) = %v, want +3.84 (losing reads positive)", got)
	}
	if got := deltaDisplay(0); got != 0 {
		t.Errorf("deltaDisplay(0) = %v, want 0", got)
	}
}

// ---- 2. Flags toggle defaults ----

func TestFlagsPanelDefaultOn(t *testing.T) {
	// Fresh install (no saved panel map): flags on.
	overlayInit(true, nil, nil, nil, 0)
	if !ovState.Flags.Load() {
		t.Error("flags must default on with no saved config")
	}
	// A preview2-era config carries every current key but "flags":
	// the pill existed then, so it must stay ON.
	overlayInit(true, map[string]bool{
		"inputs": true, "delta": true, "relative": true, "tyres": true, "engineer": true,
	}, nil, nil, 0)
	if !ovState.Flags.Load() {
		t.Error("flags must default ON when the key is absent (pre-preview3 config)")
	}
	if _, panels, _, _, _ := overlayConfig(); !panels["flags"] {
		t.Error("overlayConfig must report flags=true for the default")
	}
	// An explicit off is respected and round-trips into the config.
	overlayInit(true, map[string]bool{"flags": false}, nil, nil, 0)
	if ovState.Flags.Load() {
		t.Error("flags=false must be respected")
	}
	if _, panels, _, _, _ := overlayConfig(); panels["flags"] {
		t.Error("overlayConfig must persist flags=false")
	}
	// An explicit on, for completeness.
	overlayInit(true, map[string]bool{"flags": true}, nil, nil, 0)
	if !ovState.Flags.Load() {
		t.Error("flags=true must be respected")
	}
}

// ---- 3. Flag transition logging ----

func TestFlagLogTransitions(t *testing.T) {
	tr := &flagTracker{}
	// Nothing shown yet: no line.
	if line := tr.LogLine(FlagPill{}, false); line != "" {
		t.Fatalf("line before any pill = %q, want none", line)
	}
	// Game sector yellow appears.
	game := FlagPill{Kind: "yellow", Text: "YELLOW S1", Source: "game"}
	if line := tr.LogLine(game, true); line != "flag: YELLOW S1 (game)" {
		t.Fatalf("game yellow line = %q", line)
	}
	// Same state again: no repeat.
	if line := tr.LogLine(game, true); line != "" {
		t.Fatalf("steady state re-logged: %q", line)
	}
	// A hazard takes over the same sector: source change logs.
	haz := FlagPill{Kind: "yellow", Text: "YELLOW S1 · CAR 81m", Source: "hazard 81m"}
	if line := tr.LogLine(haz, true); line != "flag: YELLOW S1 (hazard 81m)" {
		t.Fatalf("hazard line = %q", line)
	}
	// The hazard distance ticking down is NOT a state change.
	hazNear := FlagPill{Kind: "yellow", Text: "YELLOW S1 · CAR 40m", Source: "hazard 40m"}
	if line := tr.LogLine(hazNear, true); line != "" {
		t.Fatalf("distance tick re-logged: %q", line)
	}
	// Blue replaces yellow (priority): kind change logs, with the
	// closing distance stripped from the line's state description.
	blue := FlagPill{Kind: "blue", Text: "BLUE · Hypercar 160m", Source: "closing"}
	if line := tr.LogLine(blue, true); line != "flag: BLUE · Hypercar (closing)" {
		t.Fatalf("blue line = %q", line)
	}
	blueNear := FlagPill{Kind: "blue", Text: "BLUE · Hypercar 90m", Source: "closing"}
	if line := tr.LogLine(blueNear, true); line != "" {
		t.Fatalf("blue distance tick re-logged: %q", line)
	}
	// Clearing logs exactly once.
	if line := tr.LogLine(FlagPill{}, false); line != "flag: cleared" {
		t.Fatalf("clear line = %q", line)
	}
	if line := tr.LogLine(FlagPill{}, false); line != "" {
		t.Fatalf("clear re-logged: %q", line)
	}
}

// REGRESSION (preview4): the game's raw sector flags must never
// raise the pill again. Tonight's evidence — preview3's own log,
// 2026-11-02: "flag: YELLOW S1 (game)" fired at 20:10:09 while the
// driver was still in the garage, before auto-record started
// (20:10:46), and re-asserted within 0–2 s after each
// "flag: BLUE (game)" (20:14:35→20:14:37, 20:16:50→20:16:50) —
// LMU holds the Daytona S1 sector flag yellow through normal
// green running. Every sector byte set, plus the full-course
// state, with no hazard and no blue: NO pill, and no log line.
func TestFlagGameSectorYellowIgnored(t *testing.T) {
	tr := &flagTracker{}
	s := Sample{OK: true, ElapsedTime: 10, GamePhase: 5, TrackLength: 5000,
		SectorFlags: [3]int{1, 1, 1},
		YellowState: 3,
		Field:       []CarInfo{{ID: 1, Driver: "YOU", IsPlayer: true, LapDist: 1000, Class: "LMGT3", Sector: 1, SpeedKmh: 180}}}
	p, ok := tr.Pill(s)
	if ok {
		t.Fatalf("game sector/full-course flags alone raised a pill: %+v", p)
	}
	if line := tr.LogLine(p, ok); line != "" {
		t.Fatalf("ignored flags produced a log line: %q", line)
	}
	// Still true a second later, player rolling.
	s.ElapsedTime = 11.2
	s.Field[0].LapDist = 1180
	if p, ok := tr.Pill(s); ok {
		t.Fatalf("game sector flags raised a pill on the second sample: %+v", p)
	}
}

// The full wiring: a real hazard Pill() result feeds LogLine —
// attributed (hazard 81m), and the clear is logged.
func TestFlagLogFromRealPill(t *testing.T) {
	tr := &flagTracker{}
	mk := func(et, stopped, player float64) Sample {
		return Sample{OK: true, ElapsedTime: et, GamePhase: 5, TrackLength: 5000,
			Field: []CarInfo{
				{ID: 1, Driver: "YOU", IsPlayer: true, LapDist: player, Class: "LMGT3", Sector: 1, SpeedKmh: 150},
				{ID: 2, Driver: "STOPPED", LapDist: stopped, Class: "LMGT3", Sector: 1, SpeedKmh: 0},
			}}
	}
	tr.Pill(mk(10, 1200, 1000)) // snapshot
	p, ok := tr.Pill(mk(11.1, 1201, 1120))
	if !ok || p.Kind != "yellow" || p.Source != "hazard 81m" {
		t.Fatalf("want hazard yellow, got %+v ok=%v", p, ok)
	}
	if line := tr.LogLine(p, ok); line != "flag: YELLOW S1 (hazard 81m)" {
		t.Fatalf("log line = %q", line)
	}
	// Hazard gone (the stopped car is removed from the field).
	s := mk(12.2, 0, 1300)
	s.Field = s.Field[:1]
	p, ok = tr.Pill(s)
	if ok {
		t.Fatalf("pill should be clear, got %+v", p)
	}
	if line := tr.LogLine(p, ok); line != "flag: cleared" {
		t.Fatalf("clear line = %q", line)
	}
}

// ---- 4. Blue leg: scenario, inverses, priority ----

// The field scenario from Ari's preview2 AI race: an LMGT3 player
// lapped by Hypercars got NO blue pill. This test proves the leg on
// tower-proven data alone — the player's blue byte is 0 throughout,
// so detection stands on class rank + closing geometry.
func TestFlagBlueClosingFasterClass(t *testing.T) {
	tr := &flagTracker{}
	mk := func(et, hyDist, playerDist float64) Sample {
		return Sample{OK: true, ElapsedTime: et, GamePhase: 5, TrackLength: 5000,
			Field: []CarInfo{
				{ID: 1, Driver: "YOU", IsPlayer: true, LapDist: playerDist, Class: "LMGT3", Sector: 1, SpeedKmh: 165},
				{ID: 2, Driver: "HY", LapDist: hyDist, Class: "Hypercar", Sector: 1, SpeedKmh: 205},
			}}
	}
	tr.Pill(mk(0, 1750, 2000)) // Hypercar 250 m behind
	// 1.1 s later: player covered 130 m, the Hypercar 240 m — the
	// gap is 140 m and shrinking fast.
	p, ok := tr.Pill(mk(1.1, 1990, 2130))
	if !ok || p.Kind != "blue" {
		t.Fatalf("want blue for the closing Hypercar, got %+v ok=%v", p, ok)
	}
	if !strings.Contains(p.Text, "Hypercar") || !strings.Contains(p.Text, "140m") {
		t.Errorf("blue text = %q, want class + ~140m", p.Text)
	}
	if p.Source != "closing" {
		t.Errorf("blue source = %q, want %q", p.Source, "closing")
	}
}

func TestFlagBlueSameClassNoBlue(t *testing.T) {
	tr := &flagTracker{}
	mk := func(et, carDist, playerDist float64) Sample {
		return Sample{OK: true, ElapsedTime: et, GamePhase: 5, TrackLength: 5000,
			Field: []CarInfo{
				{ID: 1, Driver: "YOU", IsPlayer: true, LapDist: playerDist, Class: "LMGT3", Sector: 1, SpeedKmh: 165},
				{ID: 2, Driver: "RIVAL", LapDist: carDist, Class: "LMGT3", Sector: 1, SpeedKmh: 205},
			}}
	}
	tr.Pill(mk(0, 1750, 2000))
	if p, ok := tr.Pill(mk(1.1, 1990, 2130)); ok {
		t.Fatalf("same-class car closing raised a pill: %+v", p)
	}
}

func TestFlagBlueSlowerClassNoBlue(t *testing.T) {
	tr := &flagTracker{}
	mk := func(et, carDist, playerDist float64) Sample {
		return Sample{OK: true, ElapsedTime: et, GamePhase: 5, TrackLength: 5000,
			Field: []CarInfo{
				{ID: 1, Driver: "YOU", IsPlayer: true, LapDist: playerDist, Class: "Hypercar", Sector: 1, SpeedKmh: 205},
				{ID: 2, Driver: "GT", LapDist: carDist, Class: "LMGT3", Sector: 1, SpeedKmh: 240},
			}}
	}
	tr.Pill(mk(0, 1750, 2000))
	if p, ok := tr.Pill(mk(1.1, 1990, 2130)); ok {
		t.Fatalf("slower-class car closing raised a pill: %+v", p)
	}
}

func TestFlagBlueFasterClassAheadNoBlue(t *testing.T) {
	tr := &flagTracker{}
	mk := func(et, hyDist, playerDist float64) Sample {
		return Sample{OK: true, ElapsedTime: et, GamePhase: 5, TrackLength: 5000,
			Field: []CarInfo{
				{ID: 1, Driver: "YOU", IsPlayer: true, LapDist: playerDist, Class: "LMGT3", Sector: 1, SpeedKmh: 165},
				{ID: 2, Driver: "HY", LapDist: hyDist, Class: "Hypercar", Sector: 1, SpeedKmh: 280},
			}}
	}
	tr.Pill(mk(0, 2400, 2000)) // Hypercar 400 m AHEAD, pulling away
	if p, ok := tr.Pill(mk(1.1, 2640, 2130)); ok {
		t.Fatalf("faster-class car ahead raised a pill: %+v", p)
	}
}

// Priority: with a stopped car ahead AND a Hypercar closing, the
// hazard yellow wins. (The fixture also sets the game's sector
// flags — inert since preview4; they change nothing.)
func TestFlagPriorityHazardBeatsBlue(t *testing.T) {
	tr := &flagTracker{}
	mk := func(et float64, stopped, hy, player float64) Sample {
		return Sample{OK: true, ElapsedTime: et, GamePhase: 5, TrackLength: 5000,
			SectorFlags: [3]int{1, 0, 0},
			Field: []CarInfo{
				{ID: 1, Driver: "YOU", IsPlayer: true, LapDist: player, Class: "LMGT3", Sector: 1, SpeedKmh: 150},
				{ID: 2, Driver: "STOPPED", LapDist: stopped, Class: "LMGT3", Sector: 1, SpeedKmh: 0},
				{ID: 3, Driver: "HY", LapDist: hy, Class: "Hypercar", Sector: 1, SpeedKmh: 210},
			}}
	}
	tr.Pill(mk(0, 1200, 800, 1000))
	p, ok := tr.Pill(mk(1.1, 1201, 960, 1120))
	if !ok || p.Kind != "yellow" || p.Source != "hazard 81m" {
		t.Fatalf("want hazard yellow to win, got %+v ok=%v", p, ok)
	}
	if !strings.Contains(p.Text, "CAR") || !strings.Contains(p.Text, "81m") {
		t.Errorf("hazard text = %q, want the car at ~81m", p.Text)
	}
}

// Blue with the game's sector flags set the whole time — Ari's
// field case (stuck game yellow, Hypercar on the bumper, no blue
// ever shown). Preview3 made blue outrank the game yellow;
// preview4 removes the game yellow as a source entirely, so blue
// fires here with the sector bytes held up throughout.
func TestFlagBlueNotStarvedBySectorFlags(t *testing.T) {
	tr := &flagTracker{}
	mk := func(et, hyDist, playerDist float64) Sample {
		return Sample{OK: true, ElapsedTime: et, GamePhase: 5, TrackLength: 5000,
			SectorFlags: [3]int{0, 1, 0},
			Field: []CarInfo{
				{ID: 1, Driver: "YOU", IsPlayer: true, LapDist: playerDist, Class: "LMGT3", Sector: 1, SpeedKmh: 165},
				{ID: 2, Driver: "HY", LapDist: hyDist, Class: "Hypercar", Sector: 1, SpeedKmh: 205},
			}}
	}
	tr.Pill(mk(0, 1750, 2000))
	p, ok := tr.Pill(mk(1.1, 1990, 2130))
	if !ok || p.Kind != "blue" {
		t.Fatalf("want blue to outrank the game yellow, got %+v ok=%v", p, ok)
	}
}
