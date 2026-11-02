package main

import (
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestOutlinePointAt(t *testing.T) {
	// Unit square loop, perimeter 4.
	o := &Outline{Track: "Sq", Pts: [][2]float64{{0, 0}, {1, 0}, {1, 1}, {0, 1}}}
	x, y := o.PointAt(0.25)
	if math.Abs(x-1) > 1e-9 || math.Abs(y-0) > 1e-9 {
		t.Errorf("PointAt(0.25) = (%.2f, %.2f), want (1, 0)", x, y)
	}
	x, y = o.PointAt(0.5)
	if math.Abs(x-1) > 1e-9 || math.Abs(y-1) > 1e-9 {
		t.Errorf("PointAt(0.5) = (%.2f, %.2f), want (1, 1)", x, y)
	}
}

func TestOutlineSaveLoad(t *testing.T) {
	dir := t.TempDir()
	o := &Outline{Track: "Test Track!"}
	for i := 0; i < 20; i++ {
		o.Pts = append(o.Pts, [2]float64{float64(i), float64(i % 3)})
	}
	if err := saveOutline(dir, o); err != nil {
		t.Fatal(err)
	}
	got, err := loadOutline(dir, "Test Track!")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Pts) != 20 {
		t.Errorf("loaded %d points, want 20", len(got.Pts))
	}
	if _, err := loadOutline(dir, "Nowhere"); err == nil {
		t.Error("loading a missing track should fail")
	}
}

func TestOutlineLearner(t *testing.T) {
	l := &outlineLearner{}
	var got *Outline
	// Drive one full lap: 100 samples 60 m apart around a loop,
	// LapDist running 0..5700 of a 5730 m track, then cross the line.
	for i := 0; i <= 100; i++ {
		a := float64(i) / 100 * 2 * math.Pi
		s := Sample{OK: true, TrackName: "Daytona", LapNumber: 3,
			LapDist: float64(i) * 57, TrackLength: 5730,
			PosX: 900 * math.Cos(a), PosZ: 900 * math.Sin(a)}
		if o := l.Observe(s); o != nil {
			t.Fatal("outline completed mid-lap")
		}
	}
	s := Sample{OK: true, TrackName: "Daytona", LapNumber: 4, LapDist: 10, TrackLength: 5730, PosX: 900, PosZ: 0}
	got = l.Observe(s)
	if got == nil {
		t.Fatal("outline did not complete at the line")
	}
	if len(got.Pts) < 40 {
		t.Errorf("outline has %d points, want >= 40", len(got.Pts))
	}
	// And it must fire exactly once.
	if o := l.Observe(s); o != nil {
		t.Error("outline fired twice")
	}
}

func TestLoadTeleSessionBothFormats(t *testing.T) {
	dir := t.TempDir()
	old := "et_s,lap,lap_dist_m,speed_kmh,throttle,brake,steering,gear,rpm,fuel_l\n"
	var sb strings.Builder
	sb.WriteString(old)
	for lap := 1; lap <= 2; lap++ {
		for i := 0; i < 60; i++ {
			et := float64(lap*100 + i)
			sb.WriteString(strings.Join([]string{
				ftoa(et), itoa(lap), ftoa(float64(i) * 90), "180", "0.5", "0", "0", "4", "7000", "40",
			}, ",") + "\n")
		}
	}
	p := filepath.Join(dir, "old.csv")
	os.WriteFile(p, []byte(sb.String()), 0644)
	laps, err := LoadTeleSession(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(laps) != 2 {
		t.Fatalf("old format: %d laps, want 2", len(laps))
	}
	if laps[0].Samples[0].HasPos {
		t.Error("old format must not claim positions")
	}

	// New format with positions.
	sb.Reset()
	sb.WriteString(csvHeader)
	for i := 0; i < 60; i++ {
		s := Sample{ElapsedTime: float64(i), LapNumber: 5, LapDist: float64(i) * 90,
			SpeedKmh: 190, Throttle: 1, Gear: 5, RPM: 7200, Fuel: 30,
			PosX: float64(i * 10), PosZ: float64(i)}
		sb.WriteString(csvRow(s))
	}
	p2 := filepath.Join(dir, "new.csv")
	os.WriteFile(p2, []byte(sb.String()), 0644)
	laps, err = LoadTeleSession(p2)
	if err != nil {
		t.Fatal(err)
	}
	if len(laps) != 1 || !laps[0].Samples[0].HasPos {
		t.Fatalf("new format: laps=%d hasPos=%v", len(laps), len(laps) == 1 && laps[0].Samples[0].HasPos)
	}
}

func TestLapDeltaAt(t *testing.T) {
	mk := func(scale float64) *TeleLap {
		l := &TeleLap{Number: 1}
		for i := 0; i <= 10; i++ {
			l.Samples = append(l.Samples, TeleSample{
				ET: float64(i) * 10 * scale, Dist: float64(i) * 100,
			})
		}
		return l
	}
	a, b := mk(1.0), mk(1.1) // B is 10% slower everywhere
	d, ok := LapDeltaAt(a, b, 500)
	if !ok {
		t.Fatal("delta not computable at 500 m")
	}
	// At 500 m: A took 50 s, B took 55 s -> A-B = -5.
	if math.Abs(d-(-5)) > 1e-6 {
		t.Errorf("delta = %.3f, want -5", d)
	}
	if _, ok := LapDeltaAt(a, b, 5000); ok {
		t.Error("delta beyond lap coverage should not be ok")
	}
}

func ftoa(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

func itoa(i int) string { return strconv.Itoa(i) }
