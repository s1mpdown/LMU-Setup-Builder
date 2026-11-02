package main

// fixround_test.go — regression tests for the v1.3.0-preview2 fix
// round, one per field failure Ari reported on preview1:
//  1. layout stretch (GUI-only; no headless test — see main.go caps)
//  2. RPM bar zoning (rpmZone proportions)
//  3. REVIEW full-lap gate + delta curve + outline fallback
//  4. delta arming + rolling-start reference poisoning (the sign
//     itself was settled a round later — see preview3_test.go)
//  5. flag pill latching on pit-lane cars
// The CSV tests replay Ari's real recordings from ~/workspace/user/
// files and skip when the files are absent (e.g. in CI).

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---- 2. RPM bar zoning ----

func TestRPMZoneLayout(t *testing.T) {
	if rpmZone(0.30) != 0 || rpmZone(0.70) != 1 || rpmZone(0.90) != 2 {
		t.Fatalf("zone boundaries wrong: %d/%d/%d", rpmZone(0.30), rpmZone(0.70), rpmZone(0.90))
	}
	// Preview1's complaint, counted: of 28 segments the red zone was
	// a single bar and orange was 3. The rebalanced bar must be
	// roughly 55% teal / 30% orange / 15% red = 15 / 8 / 5.
	counts := [3]int{}
	for i := 0; i < 28; i++ {
		counts[rpmZone(float64(i+1)/28)]++
	}
	if counts != [3]int{15, 8, 5} {
		t.Errorf("28-segment zone counts = %v, want [15 8 5]", counts)
	}
}

// ---- 5. Flag pill ----

func TestFlagPitCarsNeverTrigger(t *testing.T) {
	tr := &flagTracker{}
	mk := func(et float64, playerDist float64) Sample {
		return Sample{OK: true, ElapsedTime: et, GamePhase: 5, TrackLength: 5000,
			Field: []CarInfo{
				{ID: 1, Driver: "YOU", IsPlayer: true, LapDist: playerDist, Class: "LMGT3", Sector: 1},
				// Stationary (0 km/h, so the preview3 speed gate alone
				// would pass them) in the player's sector ahead — but
				// in the pits / in a pit state, so never a hazard.
				{ID: 2, Driver: "INPIT", LapDist: 1200, Class: "LMGT3", Sector: 1, InPits: true, SpeedKmh: 0},
				{ID: 3, Driver: "INBOX", LapDist: 1300, Class: "LMGT3", Sector: 1, PitState: 3, SpeedKmh: 0},
			}}
	}
	tr.Pill(mk(0, 1000)) // snapshot
	if p, ok := tr.Pill(mk(1.1, 1120)); ok {
		t.Fatalf("pit cars triggered the pill: %+v", p)
	}
}

func TestFlagHazardTriggersAndClears(t *testing.T) {
	tr := &flagTracker{}
	mk := func(et float64, field []CarInfo) Sample {
		return Sample{OK: true, ElapsedTime: et, GamePhase: 5, TrackLength: 5000, Field: field}
	}
	player := func(d float64) CarInfo {
		return CarInfo{ID: 1, Driver: "YOU", IsPlayer: true, LapDist: d, Class: "LMGT3", Sector: 1}
	}
	// t=0 snapshot: a STOPPED car (0 km/h — under the preview3
	// near-stopped gate) in the player's sector (ID 2) and one in a
	// different sector (ID 3).
	tr.Pill(mk(0, []CarInfo{
		player(1000),
		{ID: 2, Driver: "STOPPED", LapDist: 1200, Class: "LMGT3", Sector: 1, SpeedKmh: 0},
		{ID: 3, Driver: "ELSEWHERE", LapDist: 1200, Class: "LMGT3", Sector: 2, SpeedKmh: 0},
	}))
	// t=1.1: player advanced 120 m; both cars barely moved. Only the
	// in-sector car may trigger, at 1201-1120 = 81 m.
	p, ok := tr.Pill(mk(1.1, []CarInfo{
		player(1120),
		{ID: 2, Driver: "STOPPED", LapDist: 1201, Class: "LMGT3", Sector: 1, SpeedKmh: 0},
		{ID: 3, Driver: "ELSEWHERE", LapDist: 1201, Class: "LMGT3", Sector: 2, SpeedKmh: 0},
	}))
	if !ok || p.Kind != "yellow" {
		t.Fatalf("want yellow from the in-sector stopped car, got %+v ok=%v", p, ok)
	}
	if !strings.Contains(p.Text, "CAR") || !strings.Contains(p.Text, "81m") {
		t.Errorf("yellow text = %q, want the car at ~81m", p.Text)
	}
	if p.Source != "hazard 81m" {
		t.Errorf("yellow source = %q, want %q (transition-log attribution)", p.Source, "hazard 81m")
	}
	// t=2.2: the stopped car is gone (recovered / removed). No game
	// flag exists, so the pill must clear immediately.
	if p, ok := tr.Pill(mk(2.2, []CarInfo{player(1240)})); ok {
		t.Fatalf("pill did not clear after the hazard left: %+v", p)
	}
}

// Preview3 speed gate: a car MOVING at queue/rejoin speed in the
// player's sector must never raise the pill, even when its lap
// distance barely advances between snapshots (a pit-exit rejoiner
// crawling up the exit lane, a 50 km/h queue). Ari's stuck yellow
// arrived just after his pit stop with the game showing no yellow.
func TestFlagMovingCarNeverTriggers(t *testing.T) {
	tr := &flagTracker{}
	mk := func(et float64, carDist float64, playerDist float64) Sample {
		return Sample{OK: true, ElapsedTime: et, GamePhase: 5, TrackLength: 5000,
			Field: []CarInfo{
				{ID: 1, Driver: "YOU", IsPlayer: true, LapDist: playerDist, Class: "LMGT3", Sector: 1},
				{ID: 2, Driver: "REJOINER", LapDist: carDist, Class: "LMGT3", Sector: 1, SpeedKmh: 50},
			}}
	}
	tr.Pill(mk(0, 1200, 1000)) // snapshot
	// Player advanced 120 m; the rejoiner's lap distance crept 1 m —
	// the motion test alone would call this stopped. At 50 km/h the
	// speed gate must veto it.
	if p, ok := tr.Pill(mk(1.1, 1201, 1120)); ok {
		t.Fatalf("moving car (50 km/h) triggered the pill: %+v", p)
	}
}

// ---- 4. Delta: rolling start + sign ----

// A session joined on track (rolling start): the first tracked lap
// starts at 582 m and completes "faster" than any real lap. It must
// never become the reference, the last lap, or fuel data; the first
// FULL lap must.
func TestHistRollingStartMidLapJoin(t *testing.T) {
	resetHist()
	const TL = 5730.0
	push := func(et float64, lap int, dist float64) {
		histPush(&Sample{OK: true, ElapsedTime: et, LapNumber: lap, LapDist: dist, TrackLength: TL, Fuel: 20})
	}
	// Partial lap 5: 582 -> 5729 m in 100 s.
	for i := 0; i <= 50; i++ {
		push(1000+float64(i)*2, 5, 582+float64(i)*(5729-582)/50)
	}
	push(1102, 6, 5729) // crossing sample: lap 5 completes here
	if histBestLap() != 0 || histLastLap() != 0 {
		t.Fatalf("partial rolling-start lap was adopted: best=%.2f last=%.2f", histBestLap(), histLastLap())
	}
	// Full lap 6 at 108 s pace.
	for i := 0; i <= 200; i++ {
		push(1102.1+float64(i)*0.54, 6, float64(i)*28.65)
	}
	push(1210.2, 7, 5729) // crossing: lap 6 completes
	if best := histBestLap(); best < 107 || best > 109.5 {
		t.Fatalf("reference after first full lap = %.2f, want ~108", best)
	}
	// Lap 7: delta must be live mid-lap.
	for i := 0; i <= 100; i++ {
		push(1210.3+float64(i)*0.54, 7, float64(i)*28.65)
	}
	d, ok := histDelta(&Sample{ElapsedTime: 1210.3 + 100*0.54, LapNumber: 7, LapDist: 2865})
	if !ok {
		t.Fatal("delta not available on the lap after the first full lap")
	}
	if math.Abs(d) > 1 {
		t.Errorf("same-pace delta = %.2f, want ~0", d)
	}
}

// The sign convention, final word (v1.3.0-preview3): the panel
// displays the delta in LMU's own read — LOSING = positive/red,
// GAINING = negative/green — because Ari photographed preview2's
// panel next to the game's widget at Daytona (2026-11-02): same
// sector, game +3.844, panel −3.84. histDelta keeps the internal
// ahead-positive convention; deltaDisplay converts for the panel.
// These assertions are on the DISPLAYED value.
func TestHistDeltaSignGameConvention(t *testing.T) {
	resetHist()
	for i := 0; i <= 200; i++ { // reference lap, 0.54 s per step
		histPush(&Sample{ElapsedTime: 100 + float64(i)*0.54, LapNumber: 1, LapDist: float64(i) * 28.65, Fuel: 20})
	}
	for i := 0; i <= 100; i++ { // current lap, FASTER at 0.50 s per step
		histPush(&Sample{ElapsedTime: 208.1 + float64(i)*0.50, LapNumber: 2, LapDist: float64(i) * 28.65, Fuel: 19})
	}
	d, ok := histDelta(&Sample{ElapsedTime: 208.1 + 50, LapNumber: 2, LapDist: 2865})
	if !ok {
		t.Fatal("delta unavailable")
	}
	// At 2865 m the reference took 54.0 s; the current lap took
	// 50.0 s — GAINING 4 s. The game would show a negative (green)
	// number, so the displayed value must be ~-4, not +4.
	if disp := deltaDisplay(d); disp > -3 {
		t.Errorf("gaining displayed delta = %.2f, want ~-4 (negative = gaining, game convention)", disp)
	}

	resetHist()
	for i := 0; i <= 200; i++ { // reference lap, 0.54 s per step
		histPush(&Sample{ElapsedTime: 100 + float64(i)*0.54, LapNumber: 1, LapDist: float64(i) * 28.65, Fuel: 20})
	}
	for i := 0; i <= 100; i++ { // current lap, SLOWER at 0.58 s per step
		histPush(&Sample{ElapsedTime: 208.1 + float64(i)*0.58, LapNumber: 2, LapDist: float64(i) * 28.65, Fuel: 19})
	}
	d, ok = histDelta(&Sample{ElapsedTime: 208.1 + 58, LapNumber: 2, LapDist: 2865})
	if !ok {
		t.Fatal("delta unavailable (losing case)")
	}
	// The current lap took 58.0 s to the reference's 54.0 s —
	// LOSING 4 s reads ~+4 (red) in the game's convention.
	if disp := deltaDisplay(d); disp < 3 {
		t.Errorf("losing displayed delta = %.2f, want ~+4 (positive = losing, game convention)", disp)
	}
}

// ---- 3+4. Real-session CSV verification ----

func csvPath(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join("..", "user", "files", name)
	if _, err := os.Stat(p); err != nil {
		t.Skipf("real session file %s not present", name)
	}
	return p
}

// Replay Ari's traffic session through the live history: the
// reference must be lap 1 (108.0 s) — never the 104.3 s partial
// lap 0 the recording opened with — and the delta must be live and
// sane during lap 2.
func TestCSVTrafficHistReplay(t *testing.T) {
	p := csvPath(t, "telemetry-20261102-124939.csv")
	laps, err := LoadTeleSession(p)
	if err != nil {
		t.Fatal(err)
	}
	resetHist()
	probed := false
	lap2Seen := 0
	for _, l := range laps {
		for _, sm := range l.Samples {
			s := Sample{OK: true, ElapsedTime: sm.ET, LapNumber: sm.Lap, LapDist: sm.Dist, Fuel: sm.Fuel, TrackLength: 5725}
			histPush(&s)
			if sm.Lap == 2 {
				lap2Seen++
			}
			// Probe well into lap 2 (past the stale-head samples at
			// its start), at ~2000 m.
			if !probed && sm.Lap == 2 && lap2Seen > 50 && sm.Dist >= 1900 && sm.Dist <= 2100 {
				d, ok := histDelta(&s)
				if !ok {
					t.Fatal("delta not available during lap 2 of the traffic session")
				}
				if d < -2 || d > 2 {
					t.Errorf("lap-2 delta at 2000 m = %.2f, want a sane value near 0", d)
				}
				probed = true
			}
		}
	}
	if !probed {
		t.Fatal("never reached lap 2 at 2000 m in the replay")
	}
	if best := histBestLap(); best < 107.4 || best > 108.6 {
		t.Errorf("traffic session reference = %.2f, want lap 1's ~108.0 (not the 104.3 partial)", best)
	}
}

// The REVIEW gate across every real session file: the fastest
// selectable lap must be a genuine full lap (preview1 crowned a
// 1:30.350 fragment on the S2 file), the A-vs-B delta series must
// actually vary (preview1 rendered it flat), pedal curves must span
// the lap, and the embedded Daytona outline must parse (preview1's
// map drew nothing).
func TestCSVReviewFullLapGate(t *testing.T) {
	if o := embeddedOutline("Daytona"); o == nil || len(o.Pts) < 100 {
		t.Fatalf("embedded Daytona outline broken: %v", o)
	}
	cases := []struct {
		file        string
		wantFastest float64
	}{
		{"telemetry-20261031-190255.csv", 107.7},
		{"telemetry-20261031-192546.csv", 108.0},
		{"telemetry-20261031-215613.csv", 107.6},
		{"telemetry-20261031-221636.csv", 107.1},
		{"telemetry-20261101-143034.csv", 106.8},
		{"telemetry-20261101-151954.csv", 107.1},
		{"telemetry-20261102-124939.csv", 108.0},
	}
	for _, c := range cases {
		p := csvPath(t, c.file)
		laps, err := LoadTeleSession(p)
		if err != nil {
			t.Fatalf("%s: %v", c.file, err)
		}
		full := FullLaps(laps)
		if len(full) < 2 {
			t.Fatalf("%s: only %d full laps, want >= 2", c.file, len(full))
		}
		// Fastest + second-fastest full laps.
		var a, b *TeleLap
		for i := range full {
			if a == nil || full[i].LapTime() < a.LapTime() {
				b, a = a, &full[i]
			} else if b == nil || full[i].LapTime() < b.LapTime() {
				b = &full[i]
			}
		}
		if got := a.LapTime(); math.Abs(got-c.wantFastest) > 0.45 {
			t.Errorf("%s: fastest full lap = %.2f (lap %d), want ~%.1f", c.file, got, a.Number, c.wantFastest)
		}
		// Pedal/delta curves span the lap.
		tl := SessionTrackLength(laps)
		curve := distCurve(a)
		if curve[0].Dist > 300 || curve[len(curve)-1].Dist < 0.95*tl {
			t.Errorf("%s: lap %d curve spans %.0f..%.0f of %.0f", c.file, a.Number, curve[0].Dist, curve[len(curve)-1].Dist, tl)
		}
		// Delta series between the two best full laps must vary —
		// the flat-line regression.
		var vals []float64
		for i := 0; i <= 60; i++ {
			d := 200 + float64(i)*(0.93*tl-200)/60
			if dv, ok := LapDeltaAt(a, b, d); ok {
				vals = append(vals, dv)
			}
		}
		if len(vals) < 30 {
			t.Fatalf("%s: delta computable at only %d points", c.file, len(vals))
		}
		mean := 0.0
		for _, v := range vals {
			mean += v
		}
		mean /= float64(len(vals))
		variance := 0.0
		for _, v := range vals {
			variance += (v - mean) * (v - mean)
		}
		variance /= float64(len(vals))
		if variance < 1e-4 {
			t.Errorf("%s: delta series is flat (variance %.6f) — the stale-head bug is back", c.file, variance)
		}
	}

	// The traffic session specifically: laps 0 (mid-lap join), 3
	// (interrupted) and 4 (garage stub) are partials; only 1 and 2
	// are selectable, and lap 1 carries real positions for the map.
	p := csvPath(t, "telemetry-20261102-124939.csv")
	laps, err := LoadTeleSession(p)
	if err != nil {
		t.Fatal(err)
	}
	full := FullLaps(laps)
	if len(full) != 2 || full[0].Number != 1 || full[1].Number != 2 {
		var nums []int
		for _, l := range full {
			nums = append(nums, l.Number)
		}
		t.Fatalf("traffic full laps = %v, want [1 2]", nums)
	}
	if !full[0].Samples[0].HasPos {
		t.Error("traffic lap 1 should carry position data for the REVIEW map")
	}
}
