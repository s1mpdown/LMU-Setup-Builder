package main

import (
	"sort"
	"sync"
)

// tower.go — the timing tower's data layer: class sections, class
// intervals, and stint ages, all derived from the scoring field.
// Presentation lives in the overlay renderer; everything here is pure
// arithmetic on CarInfo so it is unit-testable anywhere.

// TowerRow is one rendered tower line.
type TowerRow struct {
	Car      CarInfo
	ClassPos int
	Interval float64 // seconds to the same-class car ahead (0 for class leader)
	Stint    int     // laps since this car's last pit stop
	DentSum  int     // telemetry dent sum for this car (0 = unhurt or unpublished)
	Elided   bool    // true for the "···" gap marker row
}

// TowerSection is one class block of the tower.
type TowerSection struct {
	Class string
	Rows  []TowerRow
}

var classRank = map[string]int{"Hypercar": 0, "HY": 0, "LMP2": 1, "LMGT3": 2, "GT3": 3}

func classOrder(c string) int {
	if r, ok := classRank[c]; ok {
		return r
	}
	return 10
}

// stintTracker remembers, per vehicle ID, the completed-lap count at
// which each car last finished a pit stop — the tower's honest fuel/VE
// proxy ("laps since the stop"). A stop is counted when the scoring
// pit-stop counter increments; stint age is measured from that lap.
type stintTracker struct {
	mu    sync.Mutex
	stops map[int]int // vehicle ID -> NumPitstops last seen
	since map[int]int // vehicle ID -> TotalLaps when the count last rose
	seen  map[int]bool
}

func newStintTracker() *stintTracker {
	return &stintTracker{stops: map[int]int{}, since: map[int]int{}, seen: map[int]bool{}}
}

var stints = newStintTracker()

func (t *stintTracker) update(field []CarInfo) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, c := range field {
		if !t.seen[c.ID] {
			t.seen[c.ID] = true
			t.stops[c.ID] = c.PitStops
			t.since[c.ID] = c.TotalLaps
			continue
		}
		if c.PitStops > t.stops[c.ID] {
			t.stops[c.ID] = c.PitStops
			t.since[c.ID] = c.TotalLaps
		}
		if c.TotalLaps < t.since[c.ID] { // session restarted under us
			t.since[c.ID] = c.TotalLaps
			t.stops[c.ID] = c.PitStops
		}
	}
}

func (t *stintTracker) age(c CarInfo) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.seen[c.ID] {
		return c.TotalLaps
	}
	a := c.TotalLaps - t.since[c.ID]
	if a < 0 {
		return 0
	}
	return a
}

// BuildTower groups the field into class sections ordered by class
// rank, assigns class positions and class intervals, and decorates
// each row with stint age and dent sum.
func BuildTower(field []CarInfo, dents map[int]int) []TowerSection {
	byClass := map[string][]CarInfo{}
	var classes []string
	for _, c := range field {
		if _, ok := byClass[c.Class]; !ok {
			classes = append(classes, c.Class)
		}
		byClass[c.Class] = append(byClass[c.Class], c)
	}
	sort.SliceStable(classes, func(i, j int) bool {
		return classOrder(classes[i]) < classOrder(classes[j])
	})
	var out []TowerSection
	for _, cls := range classes {
		cars := byClass[cls]
		sort.SliceStable(cars, func(i, j int) bool { return cars[i].Place < cars[j].Place })
		sec := TowerSection{Class: cls}
		var prev *CarInfo
		for i := range cars {
			c := cars[i]
			row := TowerRow{Car: c, ClassPos: i + 1, Stint: stints.age(c), DentSum: dents[c.ID]}
			if prev != nil {
				row.Interval = c.BehindLeader - prev.BehindLeader
				if row.Interval < 0 {
					row.Interval = 0
				}
			}
			sec.Rows = append(sec.Rows, row)
			cc := c
			prev = &cc
		}
		out = append(out, sec)
	}
	return out
}

// classFastestIdx returns the index of the row holding the class's
// fastest lap so far: the minimum valid BestLap (> 0) among the rows.
// Ties go to the first such row in class order; elided gap rows never
// hold it. Returns -1 when no row has a valid best lap yet. The tower
// renderer marks exactly this row (purple stopwatch + purple BEST).
func classFastestIdx(rows []TowerRow) int {
	best := -1
	for i, r := range rows {
		if r.Elided || r.Car.BestLap <= 0 {
			continue
		}
		if best < 0 || r.Car.BestLap < rows[best].Car.BestLap {
			best = i
		}
	}
	return best
}

// TowerWindow applies the window form: the player's class shows its
// leader pinned plus a window of rows around the player; every other
// class shows its top `otherTop` rows. Elided stretches collapse into
// a single marker row.
func TowerWindow(sections []TowerSection, around, otherTop int) []TowerSection {
	var out []TowerSection
	for _, sec := range sections {
		pi := -1
		for i, r := range sec.Rows {
			if r.Car.IsPlayer {
				pi = i
				break
			}
		}
		if pi < 0 {
			n := otherTop
			if n > len(sec.Rows) {
				n = len(sec.Rows)
			}
			rows := append([]TowerRow(nil), sec.Rows[:n]...)
			if n < len(sec.Rows) {
				rows = append(rows, TowerRow{Elided: true})
			}
			out = append(out, TowerSection{Class: sec.Class, Rows: rows})
			continue
		}
		keep := map[int]bool{0: true}
		for i := pi - around; i <= pi+around; i++ {
			if i >= 0 && i < len(sec.Rows) {
				keep[i] = true
			}
		}
		var rows []TowerRow
		lastKept := -2
		for i, r := range sec.Rows {
			if !keep[i] {
				continue
			}
			if lastKept >= 0 && i > lastKept+1 {
				rows = append(rows, TowerRow{Elided: true})
			}
			rows = append(rows, r)
			lastKept = i
		}
		if lastKept < len(sec.Rows)-1 && lastKept >= 0 {
			rows = append(rows, TowerRow{Elided: true})
		}
		out = append(out, TowerSection{Class: sec.Class, Rows: rows})
	}
	return out
}
