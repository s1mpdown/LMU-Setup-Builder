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

	// Default-plan fallback: whether any MFD menu data has EVER arrived
	// this session, and whether the current quote is a default plan.
	menuSeen  bool
	defPlan   bool
	defLogged bool

	// Strategy calls: evaluated at lap boundaries only.
	lastLapNum int
	callText   string
	callUntil  float64
	wearPrev   [4]float64
	wearRate   [4]float64
	wearInit   bool
	rainPrev   float64
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

// engLogFn is wired to the app log by main() on Windows. It stays a
// no-op elsewhere (unit tests, other platforms) and tests may swap it
// to capture lines.
var engLogFn = func(string) {}

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
	e.defPlan = false
}

// defaultEstimate prices a stop with NO MFD plan: the fallback for a
// silent pit-menu feed. Fuel to add is what the rest of the race needs
// (learned L/lap × laps remaining, less what's in the tank, capped by
// the tank); tyres are assumed ON; repairs come from the dent count.
func (e *Engineer) defaultEstimate(s Sample) float64 {
	secs := 5.0 + e.bias + e.tyreSecs
	if fpl := histFuelPerLap(); fpl > 0 {
		lapsLeft := -1.0
		if last := histLastLap(); s.SessionLeft > 0 && last > 20 {
			lapsLeft = math.Ceil(s.SessionLeft / last)
		}
		if s.MaxLaps >= s.LapNumber && s.MaxLaps <= 1000 {
			lapsLeft = float64(s.MaxLaps - s.LapNumber + 1)
		}
		if lapsLeft > 0 {
			need := lapsLeft*fpl - s.Fuel
			if need < 0 {
				need = 0
			}
			tank := tankLitres
			if s.FuelCapacity > 0 {
				tank = s.FuelCapacity
			}
			if maxAdd := tank - s.Fuel; need > maxAdd {
				need = maxAdd
			}
			secs += need / e.fillRate
		}
	}
	sum, _ := dentStats(s.Dents)
	secs += repairEstimate(sum, e.repairPerSec)
	return secs
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
		e.menuSeen = true
		e.defPlan = false
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

	// DEFAULT-PLAN FALLBACK. The 2026-11-02 log proved the pit-menu
	// feed can be silent forever (the PitInfo map opens; LMU simply
	// never delivers menu data), which used to leave the estimator
	// mute exactly when a stop was being set up. When a stop is
	// requested / the car is entering or in the pit lane and no MFD
	// selection has ever arrived, quote the default plan instead.
	// Real MFD data, if it ever comes, takes over (menuSeen above).
	if (s.PitState >= 1 || s.InPits) && !e.menuSeen && !e.planActive && !e.stopped {
		e.defPlan = true
		e.predStop = e.defaultEstimate(s)
		if !e.defLogged {
			e.defLogged = true
			engLogFn("engineer: pit menu silent - default-plan estimate in use")
		}
	}

	// Strategy calls are evaluated at lap boundaries only.
	if e.lastLapNum != 0 && s.LapNumber > e.lastLapNum {
		e.onLapBoundary(s)
	}
	if s.LapNumber > 0 {
		e.lastLapNum = s.LapNumber
	}
}

// onLapBoundary refreshes wear/rain rates and picks the single most
// urgent strategy call for the coming lap.
func (e *Engineer) onLapBoundary(s Sample) {
	if e.wearInit {
		for i := 0; i < 4; i++ {
			e.wearRate[i] = e.wearPrev[i] - s.TyreWear[i]
		}
	}
	e.wearPrev = s.TyreWear
	e.wearInit = true
	call := strategyCall(e, s)
	ttl := histLastLap()
	if ttl < 30 {
		ttl = 120
	}
	if call != "" {
		e.callText, e.callUntil = call, s.ElapsedTime+ttl*1.2
	} else {
		e.callText = ""
	}
	e.rainPrev = s.Raining
}

// classNeighbours finds the same-class cars directly ahead of and
// behind the player in the scoring field.
func classNeighbours(s Sample) (ahead, behind *CarInfo) {
	var player *CarInfo
	for i := range s.Field {
		if s.Field[i].IsPlayer {
			player = &s.Field[i]
			break
		}
	}
	if player == nil {
		return nil, nil
	}
	for i := range s.Field {
		c := &s.Field[i]
		if c.Class != player.Class || c.IsPlayer {
			continue
		}
		if c.Place < player.Place && (ahead == nil || c.Place > ahead.Place) {
			ahead = c
		}
		if c.Place > player.Place && (behind == nil || c.Place < behind.Place) {
			behind = c
		}
	}
	return ahead, behind
}

var cornerNames = [4]string{"FL", "FR", "RL", "RR"}

// strategyCall picks at most one call for the coming lap, most urgent
// first. Everything is arithmetic on live data — fuel maths only (the
// feed carries no Virtual Energy), no invented numbers.
func strategyCall(e *Engineer, s Sample) string {
	fpl := histFuelPerLap()
	last := histLastLap()
	lapsLeft := -1.0
	if s.SessionLeft > 0 && last > 20 {
		lapsLeft = math.Ceil(s.SessionLeft / last)
	}
	if s.MaxLaps >= s.LapNumber && s.MaxLaps <= 1000 {
		lapsLeft = float64(s.MaxLaps - s.LapNumber + 1)
	}
	ahead, behind := classNeighbours(s)
	windowOpen := false
	if fpl > 0 && lapsLeft > 0 {
		windowOpen = windowStatus(s.Fuel/fpl, lapsLeft, tankLitres/fpl) == "PIT WINDOW OPEN"
	}

	// 1. Fuel: short of the flag?
	if fpl > 0 && lapsLeft > 0 && s.Fuel/fpl < lapsLeft {
		savePerLap := (lapsLeft - s.Fuel/fpl) * fpl / lapsLeft
		if savePerLap <= 0.35 {
			return fmt.Sprintf("CALL - SAVE %.1f L/LAP TO THE FLAG", savePerLap)
		}
		if !windowOpen {
			return "CALL - FUEL SHORT - PLAN THE STOP"
		}
	}

	// 2. Undercut: window open, class rival close ahead, pace parity.
	if windowOpen && ahead != nil {
		gap := ahead.BehindLeader - playerBehind(s)
		if gap >= 0 && gap <= 2.0 && last > 0 && ahead.LastLap > 0 && last <= ahead.LastLap+0.3 {
			return "CALL - BOX THIS LAP - UNDERCUT " + shortName(ahead.Driver)
		}
	}

	// 3. Tyres: one corner wearing fast, or life too tight to the flag.
	if e.wearInit && lapsLeft > 0 {
		mean := (e.wearRate[0] + e.wearRate[1] + e.wearRate[2] + e.wearRate[3]) / 4
		for i := 0; i < 4; i++ {
			if mean > 0.002 && e.wearRate[i] > 1.6*mean {
				return "CALL - " + cornerNames[i] + " WEARING FAST - PROTECT IT"
			}
			if proj := s.TyreWear[i] - e.wearRate[i]*lapsLeft; proj < 0.10 && e.wearRate[i] > 0.002 {
				return "CALL - " + cornerNames[i] + " LIFE TIGHT - NURSE IT"
			}
		}
	}

	// 4. Rain trend across the last lap.
	if s.Raining-e.rainPrev >= 0.15 || (s.Raining >= 0.5 && s.Raining > e.rainPrev) {
		return "CALL - RAIN RISING - WETS NEXT STOP"
	}

	// 5. Overcut defence: faster class rival close behind, window open.
	if windowOpen && behind != nil {
		gap := playerBehind(s) - behind.BehindLeader
		if gap >= 0 && gap <= 1.5 && last > 0 && behind.LastLap > 0 && behind.LastLap < last-0.3 {
			return "CALL - STAY OUT - COVER " + shortName(behind.Driver)
		}
	}
	return ""
}

func playerBehind(s Sample) float64 {
	for i := range s.Field {
		if s.Field[i].IsPlayer {
			return s.Field[i].BehindLeader
		}
	}
	return 0
}

func shortName(d string) string {
	d = strings.TrimSpace(d)
	if len(d) > 10 {
		return d[:10]
	}
	return d
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
	if s.MaxLaps >= s.LapNumber && s.MaxLaps <= 1000 {
		lapsLeft = float64(s.MaxLaps - s.LapNumber + 1)
	}
	if fpl > 0 {
		lines = append(lines, fmt.Sprintf("FUEL %.0fL - %.1f LAPS", s.Fuel, s.Fuel/fpl))
	} else {
		lines = append(lines, fmt.Sprintf("FUEL %.0fL - LEARNING", s.Fuel))
	}
	// SETTINGS — proven-live values only. Brake bias comes from the
	// telemetry stream today (front % = (1 − rear) × 100). TC/ABS join
	// this line only when a live feed for them exists; the physics
	// "aid level" byte is NOT the car's TC selection and is not used.
	if s.RearBrakeBias > 0.05 && s.RearBrakeBias < 0.95 {
		lines = append(lines, fmt.Sprintf("BB %.1f", (1-s.RearBrakeBias)*100))
	}
	// WEATHER from the scoring header.
	wx := "DRY"
	if s.Raining > 0.35 {
		wx = "WET"
	} else if s.Raining > 0.05 {
		wx = "DAMP"
	}
	lines = append(lines, fmt.Sprintf("WX TRK %.0f AIR %.0f %s", s.TrackTemp, s.AmbientTemp, wx))
	if fpl > 0 {
		if w := windowStatus(s.Fuel/fpl, lapsLeft, tankLitres/fpl); w != "" {
			lines = append(lines, w)
		}
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
		lines = append(lines, fmt.Sprintf("DAMAGE LV%d %.0f%% - REPAIR ~%.0fs%s",
			mx, float64(sum)/16*100, repairEstimate(sum, e.repairPerSec), verdict))
	}

	if e.msg != "" && s.ElapsedTime <= e.msgUntil {
		lines = append(lines, e.msg)
	}
	if e.callText != "" && s.ElapsedTime <= e.callUntil {
		lines = append(lines, e.callText)
	}

	if e.stopped {
		lines = append(lines, fmt.Sprintf("IN BOX %.0fs - EST %.0fs", s.ElapsedTime-e.stopStart, e.predStop))
	} else if e.defPlan && e.predStop > 0 {
		lines = append(lines, fmt.Sprintf("EST STOP \u2248 %.0fs - DEFAULT PLAN", e.predStop))
	} else if e.planActive && e.predStop > 0 {
		lines = append(lines, fmt.Sprintf("STOP EST ~%.0fs", e.predStop))
	}
	if len(lines) > 8 {
		lines = lines[:8]
	}
	return lines
}
