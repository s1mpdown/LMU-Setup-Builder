package main

import "testing"

func resetHist() {
	hist.mu.Lock()
	defer hist.mu.Unlock()
	hist.recent, hist.bestTrace, hist.curTrace = nil, nil, nil
	hist.lastPushET, hist.lapStartET, hist.lapStartFuel = 0, 0, 0
	hist.lastLap, hist.trackLen = 0, 0
	hist.lap, hist.bestLap, hist.fuelPerLap = 0, 0, 0
	hist.lapTrusted = false
	hist.secS1OK, hist.secS2OK, hist.secS3OK = false, false, false
}

func pushLap(lap int, et0 float64) {
	for i := 0; i <= 200; i++ {
		histPush(&Sample{ElapsedTime: et0 + float64(i)*0.54, LapNumber: lap, LapDist: float64(i) * 28.65, Fuel: 20})
	}
}

func TestHistDeltaNeedsReferenceLap(t *testing.T) {
	resetHist()
	pushLap(1, 100)
	// Still on lap 1 / just crossed into lap 2: no completed reference yet at lap-1 time.
	if _, ok := histDelta(&Sample{ElapsedTime: 150, LapNumber: 1, LapDist: 2000}); ok {
		t.Fatal("delta should not be available before a reference lap completes")
	}
	// Lap 2 at identical pace: delta ~0 and available.
	for i := 0; i <= 100; i++ {
		histPush(&Sample{ElapsedTime: 208.27 + float64(i)*0.54, LapNumber: 2, LapDist: float64(i) * 28.65, Fuel: 19})
	}
	d, ok := histDelta(&Sample{ElapsedTime: 208.27 + 100*0.54, LapNumber: 2, LapDist: 100 * 28.65})
	if !ok {
		t.Fatal("delta should be available on lap 2")
	}
	if d < -0.5 || d > 0.5 {
		t.Fatalf("same-pace delta = %.2f, want ~0", d)
	}
}

// pushSkewedLap replays one lap the way LMU actually serves it (all six
// of Ari's Daytona CSVs): the sample where the lap counter increments
// still carries the OLD lap's end distance (~5730) for a few samples,
// then LapDist wraps to ~0. Before the trace-seeding fix, that stale
// first point blocked every real point, the lap ended with 1-2 trace
// points, and no delta reference ever armed.
func pushSkewedLap(lap int, et0 float64) {
	const trackLen = 5730.0
	// Stale tail: new lap number, old lap's end distance.
	for i := 0; i < 4; i++ {
		histPush(&Sample{ElapsedTime: et0 + float64(i)*0.02, LapNumber: lap, LapDist: trackLen, Fuel: 20})
	}
	// The flying lap proper: distance wraps and climbs.
	for i := 0; i <= 200; i++ {
		histPush(&Sample{ElapsedTime: et0 + 0.1 + float64(i)*0.54, LapNumber: lap, LapDist: float64(i) * 28.65, Fuel: 20})
	}
}

func TestHistDeltaArmsDespiteLineCrossingSkew(t *testing.T) {
	resetHist()
	pushSkewedLap(1, 100)
	pushSkewedLap(2, 208.27)
	// Lap 2 completed under skew: it must have become the reference, so
	// lap 3 shows a delta — the field bug was "SET A LAP" for lap after
	// lap, arming only on a lucky crossing.
	for i := 0; i <= 100; i++ {
		histPush(&Sample{ElapsedTime: 316.5 + float64(i)*0.54, LapNumber: 3, LapDist: float64(i) * 28.65, Fuel: 19})
	}
	if _, ok := histDelta(&Sample{ElapsedTime: 316.5 + 100*0.54, LapNumber: 3, LapDist: 100 * 28.65}); !ok {
		t.Fatal("delta must be available on the lap after a skewed crossing")
	}
}

func TestHistSurvivesSessionClockReset(t *testing.T) {
	resetHist()
	pushLap(1, 500) // old session, clock far ahead
	pushLap(2, 608)
	if _, ok := histDelta(&Sample{ElapsedTime: 650, LapNumber: 2, LapDist: 2000}); !ok {
		t.Fatal("delta should work in the first session")
	}
	// New session: the game clock restarts near zero.
	pushLap(1, 10)
	pushLap(2, 118)
	if _, ok := histDelta(&Sample{ElapsedTime: 160, LapNumber: 2, LapDist: 2000}); !ok {
		t.Fatal("delta must recover after a session clock reset")
	}
}
