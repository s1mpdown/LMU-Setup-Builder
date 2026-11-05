package main

import (
	"math"
	"testing"
)

func approx(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

func TestBiasScale(t *testing.T) {
	cases := map[float64]int{49.3: 31, 49.0: 32, 47.5: 37, 46.9: 39, 48.1: 35}
	for front, idx := range cases {
		if got := biasToIndex(front); got != idx {
			t.Errorf("biasToIndex(%.1f) = %d, want %d", front, got, idx)
		}
	}
}

func TestFuelScale(t *testing.T) {
	cases := map[float64]int{0.18: 17, 0.95: 94, 0.24: 23, 0.61: 60}
	for ratio, idx := range cases {
		if got := fuelToIndex(ratio); got != idx {
			t.Errorf("fuelToIndex(%.2f) = %d, want %d", ratio, got, idx)
		}
	}
}

func TestDaytonaRace60(t *testing.T) {
	tr := lookupTrack("Daytona")
	p := lexusPlan(tr, Session{Kind: "race60", Minutes: 60, VECap: 75}, "Balanced")
	if p.BiasFront != 49.3 {
		t.Errorf("bias = %.1f, want 49.3", p.BiasFront)
	}
	if p.Stops != 1 {
		t.Errorf("stops = %d, want 1", p.Stops)
	}
	if !approx(p.FuelRatio, 0.66, 0.03) {
		t.Errorf("fuel = %.3f, want ~0.66", p.FuelRatio)
	}
	if p.VE != 75 || p.Wing != 1 || p.Radiators != 0 {
		t.Errorf("ve/wing/rad = %d/%d/%d, want 75/1/0", p.VE, p.Wing, p.Radiators)
	}
}

func TestDaytonaQuali(t *testing.T) {
	tr := lookupTrack("Daytona")
	p := lexusPlan(tr, Session{Kind: "quali", VECap: 100}, "Balanced")
	if p.BiasFront != 49.0 {
		t.Errorf("bias = %.1f, want 49.0", p.BiasFront)
	}
	if !approx(p.FuelRatio, 0.23, 0.02) {
		t.Errorf("fuel = %.3f, want ~0.23 (the beAlien-style quali load)", p.FuelRatio)
	}
	if p.Radiators != 10 || p.Preload != 80 {
		t.Errorf("rad/preload = %d/%d, want 10/80", p.Radiators, p.Preload)
	}
}

func TestLeMansQualiFuel(t *testing.T) {
	tr := lookupTrack("LeMans_2024")
	p := lexusPlan(tr, Session{Kind: "quali", VECap: 100}, "Balanced")
	if !approx(p.FuelRatio, 0.32, 0.03) {
		t.Errorf("fuel = %.3f, want ~0.32 (4 long laps)", p.FuelRatio)
	}
}

func TestFeel(t *testing.T) {
	tr := lookupTrack("Monza")
	st := lexusPlan(tr, Session{Kind: "quali", VECap: 100}, "Stable")
	ro := lexusPlan(tr, Session{Kind: "quali", VECap: 100}, "Rotating")
	if !(st.BiasFront > 49.0 && ro.BiasFront < 49.0) {
		t.Errorf("feel bias wrong: stable %.1f rotating %.1f", st.BiasFront, ro.BiasFront)
	}
	if !(st.TCSlip > 5 && ro.TCSlip < 5) {
		t.Errorf("feel slip wrong: stable %d rotating %d", st.TCSlip, ro.TCSlip)
	}
}

// US Track Pass tracks (Ari's catch, 2026-11-05): Laguna Seca arrived
// with game v1.4 (28 Jul 2026), Road Atlanta + Long Beach with v1.4.2
// (22 Sep 2026). They must resolve to their own lengths and starting
// wing values, not fall through to the 5.70 km generic default.
func TestUSTracksLookup(t *testing.T) {
	cases := []struct {
		folder string
		km     float64
		wingQ  int
		wingR  int
	}{
		{"LagunaSeca", 3.602, 2, 3},
		{"RoadAtlanta", 4.088, 2, 2},
		{"LongBeach", 3.167, 3, 3},
	}
	for _, c := range cases {
		tr := lookupTrack(c.folder)
		if tr.Name == "Unknown track" || !approx(tr.Km, c.km, 0.001) {
			t.Errorf("lookupTrack(%q) = %q %.3f km, want %.3f km", c.folder, tr.Name, tr.Km, c.km)
		}
		if tr.WingQ != c.wingQ || tr.WingR != c.wingR {
			t.Errorf("lookupTrack(%q) wings Q/R = %d/%d, want %d/%d", c.folder, tr.WingQ, tr.WingR, c.wingQ, c.wingR)
		}
		// Lexus WingBase is 0, so the plan wing is the track wing itself.
		q := lexusPlan(tr, Session{Kind: "quali", VECap: 100}, "Balanced")
		r := lexusPlan(tr, Session{Kind: "race30", Minutes: 30, VECap: 100}, "Balanced")
		if q.Wing != c.wingQ || r.Wing != c.wingR {
			t.Errorf("%s plan wings quali/race = %d/%d, want %d/%d", tr.Name, q.Wing, r.Wing, c.wingQ, c.wingR)
		}
	}
	if len(trackTable) != 16 {
		t.Errorf("trackTable has %d tracks, want 16", len(trackTable))
	}
}

func TestBasePerLap(t *testing.T) {
	lines := []string{`FuelCapacitySetting=0//18.0L (5.3 laps)`}
	v, ok := basePerLapLitres(lines)
	if !ok || !approx(v, 3.396, 0.01) {
		t.Errorf("per-lap = %.3f ok=%v, want 3.396", v, ok)
	}
}

func TestMcLarenProfile(t *testing.T) {
	if got := mclarenProfile.BiasIndex(47.0); got != 40 {
		t.Errorf("mclaren bias idx(47.0) = %d, want 40", got)
	}
	if got := mclarenProfile.BiasIndex(48.0); got != 36 {
		t.Errorf("mclaren bias idx(48.0) = %d, want 36", got)
	}
	tr := lookupTrack("Daytona")
	q := carPlan(mclarenProfile, tr, Session{Kind: "quali", VECap: 100}, "Balanced")
	if q.BiasFront != 47.0 || q.TCSlip != 2 || q.Preload != 15 || q.Wing != 10 {
		t.Errorf("mclaren quali plan wrong: %+v", q)
	}
	r := carPlan(mclarenProfile, tr, Session{Kind: "race60", Minutes: 60, VECap: 75}, "Balanced")
	if r.BiasFront != 48.0 || r.TCSlip != 6 || r.Preload != 25 || r.Wing != 11 {
		t.Errorf("mclaren race plan wrong: %+v", r)
	}
	if !approx(r.FuelRatio, 0.66, 0.03) || r.Stops != 1 {
		t.Errorf("mclaren race fuel/stops wrong: %.3f / %d", r.FuelRatio, r.Stops)
	}
}

func TestAllProfilesGenerate(t *testing.T) {
	daytona := lookupTrack("Daytona")
	for _, p := range carProfiles {
		for _, sess := range []Session{{Kind: "quali"}, {Kind: "race", Minutes: 60, VECap: 75}} {
			plan := carPlan(p, daytona, sess, "Balanced")
			idx := p.BiasIndex(plan.BiasFront)
			if idx < 0 || idx > 90 {
				t.Errorf("%s: bias index %d out of range (bias %.1f)", p.Name, idx, plan.BiasFront)
			}
			if plan.Wing < 0 || plan.Wing > 40 {
				t.Errorf("%s: wing %d out of range", p.Name, plan.Wing)
			}
			// Anchor round-trip: asking for the anchor value returns its index.
			if got := p.BiasIndex(p.BiasValAt); got != p.BiasIdxAt {
				t.Errorf("%s: anchor round-trip %d != %d", p.Name, got, p.BiasIdxAt)
			}
		}
	}
}
