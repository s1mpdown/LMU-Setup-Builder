package main

import (
	"math"
	"strings"
	"testing"
)

func hp(s Sample) { histPush(&s) }

// The default-plan fallback: with the pit-menu feed silent, a stop
// request must still produce a labelled estimate from live data.
func TestEngineerDefaultPlanFallback(t *testing.T) {
	resetHist()
	// Teach the fuel model: lap 1 burns 50 -> 46.6 L (3.4 L/lap), 107 s.
	hp(Sample{OK: true, ElapsedTime: 1000, LapNumber: 1, Fuel: 50, LapDist: 0})
	hp(Sample{OK: true, ElapsedTime: 1107, LapNumber: 1, Fuel: 46.6, LapDist: 5700})
	hp(Sample{OK: true, ElapsedTime: 1107.1, LapNumber: 2, Fuel: 46.6, LapDist: 5, LastLap: 107})

	var logs []string
	old := engLogFn
	engLogFn = func(m string) { logs = append(logs, m) }
	defer func() { engLogFn = old }()

	e := NewEngineer()
	s := Sample{OK: true, ElapsedTime: 1200, LapNumber: 2, Fuel: 20, FuelCapacity: 100,
		SessionLeft: 1800, PitState: 1} // stop requested, menu silent
	e.Tick(s)
	if !e.defPlan {
		t.Fatal("default plan not engaged on stop request with silent menu")
	}
	// lapsLeft = ceil(1800/107) = 17; need = 17*3.4-20 = 37.8 L;
	// fill 37.8/1.6 = 23.6 s; total = 5 base + 11 tyres + 23.6 = 39.6.
	if math.Abs(e.predStop-39.6) > 0.6 {
		t.Errorf("predStop = %.2f, want ~39.6", e.predStop)
	}
	joined := strings.Join(e.Lines(s), "|")
	if !strings.Contains(joined, "DEFAULT PLAN") {
		t.Errorf("panel lines missing DEFAULT PLAN label: %s", joined)
	}
	if len(logs) != 1 || !strings.Contains(logs[0], "pit menu silent") {
		t.Errorf("expected exactly one fallback log line, got %v", logs)
	}
	// Ticking again must not log twice.
	e.Tick(s)
	if len(logs) != 1 {
		t.Errorf("fallback logged %d times, want 1", len(logs))
	}

	// Real MFD data retires the fallback.
	s2 := s
	s2.ElapsedTime = 1210
	s2.PitState = 2
	s2.MenuCat = "Fuel:"
	s2.MenuChoice = "30L"
	s2.MenuCat = "Fuel"
	e.Tick(s2)
	if e.defPlan {
		t.Error("default plan still active after MFD data arrived")
	}
	if !e.planActive {
		t.Error("real MFD plan not active after menu selection")
	}
}

// Sector deltas latch during the lap and S3 snapshots at the line.
// histPush/histSectors store the INTERNAL convention (positive =
// ahead of best); the panel displays deltaDisplay(value). Since
// v1.3.0-preview3 the displayed read is LMU's own — a sector driven
// SLOWER than the reference shows POSITIVE (red), a faster one
// NEGATIVE (green) — settled by Ari's field photo of preview2
// (2026-11-02): the game showed S1 +3.844 where our panel showed
// −3.84 for the same sector. These expectations assert the
// DISPLAYED values, i.e. what the panel actually prints.
func TestHistSectorDeltas(t *testing.T) {
	resetHist()
	base := Sample{OK: true, LapNumber: 4, BestLap: 98, BestLapS1: 30.0, BestLapS2: 62.0}
	et := 500.0
	push := func(s Sample) {
		et += 0.05
		s.ElapsedTime = et
		histPush(&s)
	}
	push(base) // first tracked sample of lap 4
	s1 := base
	s1.CurSector1 = 30.5 // 0.5 s SLOWER than the best sector
	push(s1)
	_, _, _, ok1, _, _ := histSectors()
	if !ok1 {
		t.Fatal("S1 delta did not latch")
	}
	got1, _, _, _, _, _ := histSectors()
	if disp := deltaDisplay(got1); math.Abs(disp-0.5) > 1e-9 {
		t.Errorf("S1 displayed delta = %.3f, want +0.5 (slower = positive, game convention)", disp)
	}
	s2 := base
	s2.CurSector1, s2.CurSector2 = 30.5, 61.4 // S2 split 0.6 s FASTER than best
	push(s2)
	_, got2, _, _, ok2, _ := histSectors()
	if disp := deltaDisplay(got2); !ok2 || math.Abs(disp-(-0.6)) > 1e-9 {
		t.Errorf("S2 displayed delta = %.3f ok=%v, want -0.6 (faster = negative, game convention)", disp, ok2)
	}
	// Cross the line: last lap 100 vs best 98 -> a SLOWER lap, so
	// the displayed S3 is +2.
	nx := base
	nx.LapNumber = 5
	nx.LastLap = 100
	push(nx)
	_, _, got3, _, _, ok3 := histSectors()
	if disp := deltaDisplay(got3); !ok3 || math.Abs(disp-2.0) > 1e-9 {
		t.Errorf("S3 displayed delta = %.3f ok=%v, want +2.0 (slower lap = positive)", disp, ok3)
	}
}

func TestEstTotalLaps(t *testing.T) {
	resetHist()
	if got := estTotalLaps(Sample{LapNumber: 7, MaxLaps: 40}); got != 40 {
		t.Errorf("lap-counted race: got %d, want 40", got)
	}
	hist.mu.Lock()
	hist.lastLap = 100
	hist.mu.Unlock()
	if got := estTotalLaps(Sample{LapNumber: 7, SessionLeft: 950}); got != 17 {
		t.Errorf("timed race: got %d, want 17", got)
	}
	if got := estTotalLaps(Sample{LapNumber: 7}); got != 0 {
		t.Errorf("unknown race: got %d, want 0", got)
	}
}

func TestTowerBuildAndWindow(t *testing.T) {
	field := []CarInfo{
		{ID: 1, Driver: "HY-A", Class: "Hypercar", Place: 1, BehindLeader: 0, TotalLaps: 10},
		{ID: 2, Driver: "HY-B", Class: "Hypercar", Place: 2, BehindLeader: 3, TotalLaps: 10},
		{ID: 3, Driver: "GT-A", Class: "LMGT3", Place: 3, BehindLeader: 40, TotalLaps: 9},
		{ID: 4, Driver: "GT-B", Class: "LMGT3", Place: 4, BehindLeader: 44, TotalLaps: 9},
		{ID: 5, Driver: "ARI", Class: "LMGT3", Place: 5, BehindLeader: 47, TotalLaps: 9, IsPlayer: true, PitStops: 1},
		{ID: 6, Driver: "GT-D", Class: "LMGT3", Place: 6, BehindLeader: 55, TotalLaps: 9},
		{ID: 7, Driver: "GT-E", Class: "LMGT3", Place: 7, BehindLeader: 60, TotalLaps: 9},
		{ID: 8, Driver: "GT-F", Class: "LMGT3", Place: 8, BehindLeader: 70, TotalLaps: 9},
	}
	dents := map[int]int{4: 5}
	secs := BuildTower(field, dents)
	if len(secs) != 2 || secs[0].Class != "Hypercar" || secs[1].Class != "LMGT3" {
		t.Fatalf("sections wrong: %+v", secs)
	}
	gt := secs[1]
	if gt.Rows[2].ClassPos != 3 || gt.Rows[2].Car.Driver != "ARI" {
		t.Errorf("player class position wrong: %+v", gt.Rows[2])
	}
	if math.Abs(gt.Rows[1].Interval-4) > 1e-9 {
		t.Errorf("GT-B interval = %.2f, want 4", gt.Rows[1].Interval)
	}
	if gt.Rows[1].DentSum != 5 {
		t.Errorf("GT-B dent sum = %d, want 5", gt.Rows[1].DentSum)
	}
	win := TowerWindow(secs, 1, 2)
	// Player class: leader pinned + player±1, contiguous here except
	// the tail => GT-A, GT-B, ARI, GT-D, ···
	gtw := win[1].Rows
	if len(gtw) != 5 {
		t.Fatalf("window rows = %d, want 5: %+v", len(gtw), gtw)
	}
	if gtw[1].Elided || !gtw[4].Elided {
		t.Errorf("elision markers misplaced: %+v", gtw)
	}
	// Other class: top 2 + elision absent (only 2 cars).
	if len(win[0].Rows) != 2 {
		t.Errorf("hypercar window = %d rows, want 2", len(win[0].Rows))
	}
}

func TestStintTracker(t *testing.T) {
	tr := newStintTracker()
	car := CarInfo{ID: 9, PitStops: 0, TotalLaps: 5}
	tr.update([]CarInfo{car})
	if got := tr.age(car); got != 0 {
		t.Errorf("first-seen stint = %d, want 0", got)
	}
	car.TotalLaps = 12
	tr.update([]CarInfo{car})
	if got := tr.age(car); got != 7 {
		t.Errorf("stint = %d, want 7", got)
	}
	car.PitStops = 1 // stop completed during lap 12
	tr.update([]CarInfo{car})
	car.TotalLaps = 15
	tr.update([]CarInfo{car})
	if got := tr.age(car); got != 3 {
		t.Errorf("post-stop stint = %d, want 3", got)
	}
}

func TestStrategyFuelSaveCall(t *testing.T) {
	resetHist()
	hp(Sample{OK: true, ElapsedTime: 1000, LapNumber: 1, Fuel: 50})
	hp(Sample{OK: true, ElapsedTime: 1107, LapNumber: 1, Fuel: 46.6})
	hp(Sample{OK: true, ElapsedTime: 1107.1, LapNumber: 2, Fuel: 46.6, LastLap: 107})
	e := NewEngineer()
	// 8.8 laps of fuel, 9 laps to run -> tiny save target.
	s := Sample{OK: true, ElapsedTime: 1200, LapNumber: 2, Fuel: 30, SessionLeft: 963}
	call := strategyCall(e, s)
	if !strings.HasPrefix(call, "CALL - SAVE") {
		t.Errorf("call = %q, want a SAVE call", call)
	}
}
