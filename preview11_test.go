package main

import "testing"

// preview11: the tower's fastest-lap marker decision. The renderer
// paints a purple stopwatch beside the driver name and the BEST time
// in purple on exactly the row classFastestIdx picks per class
// section; these tests pin the picking rules.

func flRow(driver string, best float64, player bool) TowerRow {
	return TowerRow{Car: CarInfo{Driver: driver, BestLap: best, IsPlayer: player}}
}

func TestClassFastestNormal(t *testing.T) {
	rows := []TowerRow{
		flRow("A", 108.5, false),
		flRow("B", 106.9, false),
		flRow("C", 107.3, false),
	}
	if got := classFastestIdx(rows); got != 1 {
		t.Errorf("classFastestIdx = %d, want 1 (B, 106.9)", got)
	}
}

func TestClassFastestTieGoesToFirst(t *testing.T) {
	rows := []TowerRow{
		flRow("A", 107.0, false),
		flRow("B", 106.5, false),
		flRow("C", 106.5, false),
	}
	if got := classFastestIdx(rows); got != 1 {
		t.Errorf("classFastestIdx = %d, want 1 (first of the tied 106.5s)", got)
	}
}

func TestClassFastestAllInvalid(t *testing.T) {
	rows := []TowerRow{
		flRow("A", 0, false),
		flRow("B", 0, false),
		flRow("C", 0, false),
	}
	if got := classFastestIdx(rows); got != -1 {
		t.Errorf("classFastestIdx = %d, want -1 (no valid bests)", got)
	}
	if got := classFastestIdx(nil); got != -1 {
		t.Errorf("classFastestIdx(nil) = %d, want -1", got)
	}
}

func TestClassFastestPlayerHoldsIt(t *testing.T) {
	rows := []TowerRow{
		flRow("LEADER", 107.2, false),
		flRow("YOU", 106.8, true),
		flRow("BEHIND", 107.9, false),
	}
	if got := classFastestIdx(rows); got != 1 {
		t.Errorf("classFastestIdx = %d, want 1 (the player)", got)
	}
}

func TestClassFastestSkipsElidedAndInvalid(t *testing.T) {
	rows := []TowerRow{
		{Elided: true},
		flRow("A", 0, false), // no lap set yet — not a holder
		flRow("B", 107.4, false),
		flRow("C", 107.1, false),
	}
	if got := classFastestIdx(rows); got != 3 {
		t.Errorf("classFastestIdx = %d, want 3 (C, 107.1)", got)
	}
}
