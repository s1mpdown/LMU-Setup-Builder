package main

import "testing"

// Preview5's only change: while the pit limiter is engaged, the
// Inputs RPM bar's lit segments pulse between limiter blue and
// paper white instead of holding one flat blue. The render side
// (overlay_windows.go) picks colLimiter on the true phase and
// colPaper on the false phase; these tests pin the phase itself —
// ~167 ms per colour, i.e. a ~3 Hz blue/white cycle — and that it
// keeps alternating rather than sticking.
func TestRPMLimiterFlashPhases(t *testing.T) {
	cases := []struct {
		ms   int64
		want bool
	}{
		{0, true},    // first (blue) phase opens the cycle
		{166, true},  // still blue just before the boundary
		{167, false}, // white phase
		{333, false}, // still white just before the next boundary
		{334, true},  // blue again — one full ~334 ms cycle done
		{501, false}, // and it keeps alternating
		{668, true},
	}
	for _, c := range cases {
		if got := rpmLimiterFlash(c.ms); got != c.want {
			t.Errorf("rpmLimiterFlash(%d) = %v, want %v", c.ms, got, c.want)
		}
	}
}

// Over any full second the phase must visit both colours several
// times (~3 full cycles), never freezing on one.
func TestRPMLimiterFlashKeepsCycling(t *testing.T) {
	blues, whites := 0, 0
	for ms := int64(0); ms < 1000; ms += 10 {
		if rpmLimiterFlash(ms) {
			blues++
		} else {
			whites++
		}
	}
	if blues == 0 || whites == 0 {
		t.Fatalf("phase froze over 1 s: blue samples %d, white samples %d", blues, whites)
	}
}
