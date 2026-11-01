package main

import "testing"

func resetHist() {
	hist.mu.Lock()
	defer hist.mu.Unlock()
	hist.recent, hist.bestTrace, hist.curTrace = nil, nil, nil
	hist.lastPushET, hist.lapStartET, hist.lapStartFuel = 0, 0, 0
	hist.lap, hist.bestLap, hist.fuelPerLap = 0, 0, 0
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
