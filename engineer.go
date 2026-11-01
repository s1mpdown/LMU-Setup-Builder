package main

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
)

// The race engineer: a deterministic strategy brain fed by the same live
// feed as the overlay. It watches fuel burn, the session clock, the pit
// MFD (via the PitInfo buffer), damage, and the pit state — and turns them
// into short text calls on the Engineer panel. No LLM in the loop: strategy
// is arithmetic with good timing, and the arithmetic must never lag.
//
// The stop plan is gathered from the MFD whenever Ari browses it — drivers
// set their stop up laps in advance, so selections are NOT tied to the pit
// phase. They are consumed (cleared) when a stop is served. Fuel amounts
// in the MFD are litres TO ADD (rF2 lineage), not a target total.

type Engineer struct {
	mu sync.Mutex

	// Stop-time model. Constants start as sane LMU estimates; the bias is
	// learned from Ari's real stops (measured stationary time vs estimate).
	bias         float64
	fillRate     float64 // litres per second
	tyreSecs     float64
	repairPerSec float64 // seconds per dent point
	adjSecs      float64

	msg      string
	msgUntil float64

	// The pending stop plan, from MFD browsing at any time.
	fuelAdd    float64 // litres to add; -1 = not set
	tyresSet   bool
	repairSet  bool
	adjCats    map[string]bool
	planDirty  bool
	predStop   float64
	lastMenu   string
	planActive bool // any selection made

	inPitsNow bool
	stopped   bool
	stopStart float64

	lastDentSum, lastDentMax int
	flatSeen                 bool
}

func NewEngineer() *Engineer {
	return &Engineer{
		fillRate:     1.6,
		tyreSecs:     11,
		repairPerSec: 3.5,
		adjSecs:      2,
		fuelAdd:      -1,
		adjCats:      map[string]bool{},
	}
}

var eng = NewEngineer()

func dentStats(d [8]int) (sum, max int) {
	for _, v := range d {
		sum += v
		if v > max {
			max = v
		}
	}
	return
}

func repairEstimate(dentSum int, per float64) float64 {
	e := float64(dentSum) * per
	if e > 60 {
		e = 60
	}
	return e
}

// windowStatus is the pit-window call for a timed race.
func windowStatus(lapsFuel, lapsLeft, lapsFull float64) string {
	if lapsLeft < 0 {
		return ""
	}
	if lapsFuel >= lapsLeft {
		return "NO STOP NEEDED"
	}
	if lapsLeft <= lapsFull {
		return "PIT WINDOW OPEN"
	}
	return fmt.Sprintf("WINDOW IN %d LAPS", int(math.Ceil(lapsLeft-lapsFull)))
}

func menuKind(cat string) string {
	c := strings.ToUpper(cat)
	switch {
	case strings.Contains(c, "FUEL"), strings.Contains(c, "ENERGY"),
		strings.Contains(c, "REFUEL"), strings.Contains(c, "PETROL"),
		strings.Contains(c, "GAS"):
		return "fuel"
	case strings.Contains(c, "TIRE"), strings.Contains(c, "TYRE"):
		return "tyres"
	case strings.Contains(c, "REPAIR"), strings.Contains(c, "DAMAGE"):
		return "repair"
	case strings.Contains(c, "DUCT"), strings.Contains(c, "WING"),
		strings.Contains(c, "PRESSURE"), strings.Contains(c, "BRAKE"),
		strings.Contains(c, "DIFF"), strings.Contains(c, "AERO"),
		strings.Contains(c, "SPOILER"), strings.Contains(c, "FLAP"):
		return "adjust"
	}
	return ""
}

func parseMenuAmount(choice string) (float64, bool) {
	fields := strings.FieldsFunc(choice, func(r rune) bool {
		return !(r >= '0' && r <= '9' || r == '.')
	})
	for _, f := range fields {
		if v, err := strconv.ParseFloat(f, 64); err == nil {
			return v, true
		}
	}
	return 0, false
}

func (e *Engineer) say(now float64, msg string, secs float64) {
	e.msg = msg
	e.msgUntil = now + secs
}

func (e *Engineer) estimate(s Sample) float64 {
	secs := 5.0 + e.bias
	if e.tyresSet {
		secs += e.tyreSecs
	}
	if e.fuelAdd > 0 {
		secs += e.fuelAdd / e.fillRate
	}
	if e.repairSet {
		sum, _ := dentStats(s.Dents)
		secs += repairEstimate(sum, e.repairPerSec)
	}
	secs += float64(len(e.adjCats)) * e.adjSecs
	return secs
}

func (e *Engineer) clearPlan() {
	e.fuelAdd = -1
	e.tyresSet, e.repairSet = false, false
	e.adjCats = map[string]bool{}
	e.planActive = false
}

func anyFlat(s Sample) bool {
	for _, f := range s.TyreFlat {
		if f {
			return true
		}
	}
	for _, f := range s.TyreOff {
		if f {
			return true
		}
	}
	return false
}

// Tick folds one live sample into the engineer's state: damage events,
// MFD watching (any time — the plan is set up in advance), stop timing
// and learning. Called from the telemetry loop.
func (e *Engineer) Tick(s Sample) {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := s.ElapsedTime
	sum, mx := dentStats(s.Dents)

	if sum > 0 && (mx > e.lastDentMax || sum >= e.lastDentSum+2) {
		e.say(now, fmt.Sprintf("IMPACT - DAMAGE LV%d", mx), 15)
	}
	if anyFlat(s) && !e.flatSeen {
		e.say(now, "PUNCTURE - BOX WORTH IT", 20)
	}
	e.flatSeen = anyFlat(s)
	e.lastDentSum, e.lastDentMax = sum, mx

	// MFD watching: fold whatever he is looking at into the pending plan.
	if s.MenuCat != "" {
		kind := menuKind(s.MenuCat)
		choiceOff := strings.Contains(strings.ToUpper(s.MenuChoice), "NO")
		switch kind {
		case "fuel":
			if v, ok := parseMenuAmount(s.MenuChoice); ok {
				if strings.Contains(s.MenuChoice, "%") {
					e.fuelAdd = v / 100 * tankLitres
				} else {
					e.fuelAdd = v
				}
				e.planActive = true
			}
		case "tyres":
			e.tyresSet = !choiceOff
			e.planActive = true
		case "repair":
			e.repairSet = !choiceOff
			e.planActive = true
		case "adjust":
			e.adjCats[s.MenuCat] = true
			e.planActive = true
		case "":
			// An MFD page we don't recognise still costs crew time if he
			// changes it during the stop — count it as an adjustment
			// rather than pretending the stop is free.
			e.adjCats[s.MenuCat] = true
			e.planActive = true
		}
		e.predStop = e.estimate(s)
		key := s.MenuCat + "|" + s.MenuChoice
		if key != e.lastMenu {
			e.say(now, fmt.Sprintf("STOP EST %.0fs", e.predStop), 10)
		}
		e.lastMenu = key
	}

	if s.PitState == 3 && !e.stopped {
		e.stopped = true
		e.stopStart = now
	}
	if s.PitState != 3 && e.stopped {
		e.stopped = false
		if measured := now - e.stopStart; measured > 3 && e.predStop > 0 {
			e.bias = clampF(e.bias+0.5*(measured-e.predStop), -8, 10)
		}
		e.clearPlan() // the plan has been served
	}
	e.inPitsNow = s.PitState > 0 || s.InPits
}

// Lines renders the panel: status first, then the live call, then the
// pit-stop picture.
func (e *Engineer) Lines(s Sample) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var lines []string

	fpl := histFuelPerLap()
	last := histLastLap()
	lapsLeft := -1.0
	if s.SessionLeft > 0 && last > 20 {
		lapsLeft = math.Ceil(s.SessionLeft / last)
	}
	if fpl > 0 {
		lines = append(lines, fmt.Sprintf("FUEL %.0fL - %.1f LAPS", s.Fuel, s.Fuel/fpl))
		if w := windowStatus(s.Fuel/fpl, lapsLeft, tankLitres/fpl); w != "" {
			lines = append(lines, w)
		}
	} else {
		lines = append(lines, fmt.Sprintf("FUEL %.0fL - LEARNING", s.Fuel))
	}

	sum, mx := dentStats(s.Dents)
	if sum > 0 && !e.inPitsNow {
		verdict := ""
		switch {
		case lapsLeft >= 6:
			verdict = " - FIX AT STOP"
		case lapsLeft >= 0:
			verdict = " - NURSE IT HOME"
		}
		lines = append(lines, fmt.Sprintf("DAMAGE LV%d - REPAIR ~%.0fs%s", mx, repairEstimate(sum, e.repairPerSec), verdict))
	}

	if e.msg != "" && s.ElapsedTime <= e.msgUntil {
		lines = append(lines, e.msg)
	}

	if s.MenuCat != "" {
		m := fmt.Sprintf("MFD %s > %s", s.MenuCat, s.MenuChoice)
		if len(m) > 42 {
			m = m[:42]
		}
		lines = append(lines, m)
	}
	if e.stopped {
		lines = append(lines, fmt.Sprintf("IN BOX %.0fs - EST %.0fs", s.ElapsedTime-e.stopStart, e.predStop))
	} else if e.planActive && e.predStop > 0 {
		lines = append(lines, fmt.Sprintf("STOP EST ~%.0fs", e.predStop))
	}
	if len(lines) > 5 {
		lines = lines[:5]
	}
	return lines
}
