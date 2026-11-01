package main

import (
	"strings"
	"testing"
)

func TestWindowStatus(t *testing.T) {
	cases := []struct {
		fuel, left, full float64
		want             string
	}{
		{30, 20, 28, "NO STOP NEEDED"},
		{10, 20, 28, "PIT WINDOW OPEN"},
		{10, 40, 28, "WINDOW IN 12 LAPS"},
		{10, -1, 28, ""},
	}
	for _, c := range cases {
		if got := windowStatus(c.fuel, c.left, c.full); got != c.want {
			t.Errorf("windowStatus(%.0f, %.0f, %.0f) = %q, want %q", c.fuel, c.left, c.full, got, c.want)
		}
	}
}

func TestMenuParsing(t *testing.T) {
	if menuKind("FUEL") != "fuel" || menuKind("Tires") != "tyres" ||
		menuKind("REPAIR ALL") != "repair" || menuKind("BRAKE DUCTS") != "adjust" ||
		menuKind("GRILL") != "" {
		t.Fatal("menuKind misclassified")
	}
	if v, ok := parseMenuAmount("Add 25 L"); !ok || v != 25 {
		t.Fatalf("parseMenuAmount = %.1f %v", v, ok)
	}
	if _, ok := parseMenuAmount("None"); ok {
		t.Fatal("parseMenuAmount should fail on text-only choice")
	}
}

func TestEstimateAndLearning(t *testing.T) {
	e := NewEngineer()
	// He sets the stop up laps in advance, still out on track: tyres + 20 L.
	e.Tick(Sample{Fuel: 60, ElapsedTime: 100, MenuCat: "TIRES", MenuChoice: "All"})
	e.Tick(Sample{Fuel: 60, ElapsedTime: 101, MenuCat: "FUEL", MenuChoice: "20 L"})
	// 5 base + 11 tyres + 20L/1.6 fill = 28.5
	if e.predStop != 28.5 {
		t.Fatalf("predStop = %.1f, want 28.5", e.predStop)
	}
	// The real stop takes 38.5s stationary: 10 over estimate, bias learns +5.
	e.Tick(Sample{PitState: 3, Fuel: 60, ElapsedTime: 200})
	e.Tick(Sample{PitState: 0, Fuel: 80, ElapsedTime: 238.5})
	if e.bias != 5 {
		t.Fatalf("bias = %.1f, want 5", e.bias)
	}
	if e.planActive {
		t.Fatal("plan should be cleared once the stop is served")
	}
}

func TestWingAndFuelQuote(t *testing.T) {
	// Ari's report: 1 wing click + 20 L must not quote the 5s bare base.
	e := NewEngineer()
	e.Tick(Sample{Fuel: 10, ElapsedTime: 50, MenuCat: "REAR WING", MenuChoice: "8.0 deg"})
	e.Tick(Sample{Fuel: 10, ElapsedTime: 51, MenuCat: "FUEL", MenuChoice: "20"})
	// 5 base + 2 adjustment + 20/1.6 fill = 19.5
	if e.predStop != 19.5 {
		t.Fatalf("predStop = %.1f, want 19.5", e.predStop)
	}
}

func TestDamageAlertAndLines(t *testing.T) {
	resetHist()
	hist.mu.Lock()
	hist.fuelPerLap = 3.6
	hist.mu.Unlock()
	e := NewEngineer()
	s := Sample{Fuel: 36, ElapsedTime: 500, SessionLeft: -1}
	e.Tick(s)
	var d Sample = s
	d.Dents[2] = 2
	d.ElapsedTime = 501
	e.Tick(d)
	lines := e.Lines(d)
	joined := strings.Join(lines, "|")
	if !strings.Contains(joined, "FUEL 36L - 10.0 LAPS") {
		t.Fatalf("fuel line missing: %v", lines)
	}
	if !strings.Contains(joined, "DAMAGE LV2") {
		t.Fatalf("damage line missing: %v", lines)
	}
	if e.msg == "" {
		t.Fatal("impact should set an engineer message")
	}
}
