package main

import (
	"strings"
	"testing"
)

func TestFlagGreenFlash(t *testing.T) {
	tr := &flagTracker{}
	base := Sample{OK: true, ElapsedTime: 100, GamePhase: 4, TrackLength: 5000}
	if _, ok := tr.Pill(base); ok {
		t.Fatal("pill active before green")
	}
	base.GamePhase = 5
	p, ok := tr.Pill(base)
	if !ok || p.Kind != "green" {
		t.Fatalf("want green flash, got %+v ok=%v", p, ok)
	}
	base.ElapsedTime = 104 // flash is 3.5 s
	if _, ok := tr.Pill(base); ok {
		t.Error("green flash should have expired")
	}
}

func TestFlagYellowSlowCar(t *testing.T) {
	tr := &flagTracker{}
	mk := func(et float64, slowDist float64, playerDist float64) Sample {
		// Raw mSector 2 displays as S2 (header: 0=S3, 1=S1, 2=S2).
		// The sector in the pill text is the hazard's own sector —
		// the game's sector-flag array is not a pill source anymore
		// (preview4) and is deliberately absent from this fixture.
		return Sample{OK: true, ElapsedTime: et, GamePhase: 5, TrackLength: 5000,
			Field: []CarInfo{
				{ID: 1, Driver: "YOU", IsPlayer: true, LapDist: playerDist, Class: "LMGT3", Sector: 2},
				{ID: 2, Driver: "STOPPED", LapDist: slowDist, Class: "LMGT3", Sector: 2, SpeedKmh: 0},
			}}
	}
	// t=0: snapshot. Player at 1000, slow car at 1200.
	tr.Pill(mk(0, 1200, 1000))
	// t=1.1: player advanced 120 m; the car ahead moved 1 m.
	p, ok := tr.Pill(mk(1.1, 1201, 1120))
	if !ok || p.Kind != "yellow" {
		t.Fatalf("want yellow, got %+v ok=%v", p, ok)
	}
	if !strings.Contains(p.Text, "S2") || !strings.Contains(p.Text, "81m") {
		t.Errorf("yellow text = %q, want sector + ~81m", p.Text)
	}
}

func TestFlagBlueClosing(t *testing.T) {
	tr := &flagTracker{}
	mk := func(et float64, hyDist float64, playerDist float64) Sample {
		return Sample{OK: true, ElapsedTime: et, GamePhase: 5, TrackLength: 5000,
			Field: []CarInfo{
				{ID: 1, Driver: "YOU", IsPlayer: true, LapDist: playerDist, Class: "LMGT3", Flag: 6},
				{ID: 2, Driver: "HY", LapDist: hyDist, Class: "Hypercar"},
			}}
	}
	tr.Pill(mk(0, 800, 1000)) // hypercar 200 m behind
	p, ok := tr.Pill(mk(1.1, 960, 1120))
	if !ok || p.Kind != "blue" {
		t.Fatalf("want blue, got %+v ok=%v", p, ok)
	}
	if !strings.Contains(p.Text, "Hypercar") || !strings.Contains(p.Text, "160m") {
		t.Errorf("blue text = %q, want class + 160m", p.Text)
	}
}
