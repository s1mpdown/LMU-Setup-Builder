package main

import (
	"fmt"
	"strings"
)

// flags.go — the flag pill's brain. One pill, top-centre, exists only
// while a flag is actually active:
//   - GREEN: flashes for a few seconds when running goes green
//     (session start / restart), then disappears.
//   - YELLOW: ONLY a near-stopped on-track car identified ahead in
//     the player's sector, with its live distance. (Preview4: the
//     game's raw sector-flag array used to be a yellow source too,
//     but preview3's transition log convicted it — LMU holds the
//     Daytona S1 sector flag yellow essentially permanently during
//     green running, pit/garage activity inside the sector span,
//     and the pill relayed it even before the car left the garage.
//     The game's own HUD ignores that byte; so do we now.)
//   - BLUE: a faster-class car is closing on the player from behind
//     (detected from the field's classes and lap distances; the
//     game's own blue byte also qualifies) — names the class of the
//     closing car and its live distance behind.
// When several apply at once the hazard yellow wins, then blue.
//
// Slow/closing detection compares scoring lap-distances against a
// ~1 s-old snapshot: a car ahead that barely moved while the player
// covered ground is stopped/slow; a car behind whose gap shrank is
// closing. Everything degrades gracefully: no identified car means
// the sector/class-only form.

type FlagPill struct {
	Kind string // "green" | "yellow" | "blue"
	Text string
	// Source attributes the pill for the app-log transition lines
	// (v1.3.0-preview3 diagnosis): "hazard <d>m" when our stopped-car
	// detection raised it, "closing" for a detected faster-class
	// car, "game" for the green flash / the game's own blue byte.
	// Purely diagnostic — it never influences when the pill fires.
	Source string
}

type flagTracker struct {
	phaseInit  bool
	prevPhase  int
	greenUntil float64

	snapET     float64
	snapDist   map[int]float64
	snapPlayer float64

	logLast string // last flag state written to the app log (coarse key)
}

// flagLogBase is the pill text minus its volatile distance
// segments (" · CAR 81m" on a hazard yellow, the trailing " 160m"
// on a blue): distances tick as gaps change, and the transition
// log must not chase them.
func flagLogBase(text string) string {
	if i := strings.Index(text, " · CAR "); i >= 0 {
		text = text[:i]
	}
	if i := strings.LastIndex(text, " "); i >= 0 {
		tok := text[i+1:]
		if strings.HasSuffix(tok, "m") {
			num := tok[:len(tok)-1]
			digits := num != ""
			for _, r := range num {
				if r < '0' || r > '9' {
					digits = false
				}
			}
			if digits {
				text = text[:i]
			}
		}
	}
	return text
}

// fasterClass reports whether class a is a strictly faster tier
// than class b, on the tower's class ranking — the tower groups
// the live field by these same CarInfo.Class strings, so the
// ordering is field-proven. An unknown class on either side
// proves nothing and never counts as faster.
func fasterClass(a, b string) bool {
	ra, rb := classOrder(a), classOrder(b)
	return ra < rb && ra < 10 && rb < 10
}

// LogLine returns the app-log line for a flag-pill state change, or
// "" when the displayed state is unchanged since the last call. The
// change key is coarse — kind + base text + source CLASS (game vs
// hazard) — so a hazard's shrinking distance doesn't spam the log,
// while a new sector, a new source, or the pill clearing each log
// exactly once. Lines read like the pill itself:
// "flag: BLUE (game)", "flag: YELLOW S1 (hazard 81m)",
// "flag: cleared". The renderer writes non-empty lines via logLine.
func (t *flagTracker) LogLine(p FlagPill, active bool) string {
	if !active {
		if t.logLast == "" {
			return ""
		}
		t.logLast = ""
		return "flag: cleared"
	}
	srcClass := "game"
	if strings.HasPrefix(p.Source, "hazard") {
		srcClass = "hazard"
	} else if strings.HasPrefix(p.Source, "closing") {
		srcClass = "closing"
	}
	key := p.Kind + "|" + flagLogBase(p.Text) + "|" + srcClass
	if key == t.logLast {
		return ""
	}
	t.logLast = key
	src := p.Source
	if src == "" {
		src = "game"
	}
	return "flag: " + flagLogBase(p.Text) + " (" + src + ")"
}

var flags = &flagTracker{}

func (t *flagTracker) snapshot(s Sample) {
	t.snapET = s.ElapsedTime
	t.snapDist = map[int]float64{}
	for _, c := range s.Field {
		t.snapDist[c.ID] = c.LapDist
		if c.IsPlayer {
			t.snapPlayer = c.LapDist
		}
	}
}

// wrapAhead returns c's distance ahead of the player in metres,
// wrapped into (0, trackLen).
func wrapAhead(player, other, trackLen float64) float64 {
	d := other - player
	if trackLen > 0 {
		for d < 0 {
			d += trackLen
		}
		for d >= trackLen {
			d -= trackLen
		}
	}
	return d
}

func (t *flagTracker) Pill(s Sample) (FlagPill, bool) {
	if !t.phaseInit {
		t.phaseInit = true
		t.prevPhase = s.GamePhase
		t.snapshot(s)
	}
	// Green flash on the transition into green running.
	if t.prevPhase != 5 && s.GamePhase == 5 && t.prevPhase >= 0 {
		t.greenUntil = s.ElapsedTime + 3.5
	}
	t.prevPhase = s.GamePhase
	if s.ElapsedTime <= t.greenUntil && s.GamePhase == 5 {
		return FlagPill{Kind: "green", Text: "GREEN", Source: "game"}, true
	}

	// Refresh the motion snapshot about once a second.
	if t.snapDist == nil || s.ElapsedTime-t.snapET >= 1.0 {
		defer t.snapshot(s)
	}
	playerMoved := 0.0
	var player *CarInfo
	for i := range s.Field {
		if s.Field[i].IsPlayer {
			player = &s.Field[i]
			break
		}
	}
	if player != nil && t.snapDist != nil {
		playerMoved = wrapAhead(t.snapPlayer, player.LapDist, s.TrackLength)
		if playerMoved > s.TrackLength/2 { // wrapped past the line oddly
			playerMoved = 0
		}
	}

	// YELLOW — one source only (preview4): a stopped/slow car
	// identified ahead IN THE PLAYER'S SECTOR. It is fenced so it
	// cannot latch in traffic: cars in the pits or in any pit state
	// (entering, stopped in the box, exiting) never count — in a
	// 62-car session there are ALWAYS stationary cars there — only
	// cars in the player's own sector count, and (preview3, after
	// Ari's post-pit-stop false yellow) the car must be NEAR-STOPPED:
	// PitState returns to 0 at the pit-exit line while a rejoiner is
	// still crawling up to speed, and slow traffic queues roll at
	// 40+ km/h — neither is a hazard. Only a car under ~15 km/h (a
	// genuinely stopped or spun car) may raise the pill; the motion
	// snapshot test below stays as the second fence. The pill is
	// recomputed from the current sample on every call, so it clears
	// on the first frame where the hazard no longer fires.
	//
	// The game's sector-flag array (Sample.SectorFlags) and the
	// full-course state are deliberately NOT consulted anymore:
	// preview3's log showed "flag: YELLOW S1 (game)" firing in the
	// garage before the session even started and re-asserting
	// within seconds of every blue — LMU holds that sector flag
	// yellow through normal green running at Daytona. The fields
	// stay parsed for the regression test that pins this decision.
	hazardDist := -1.0
	if player != nil {
		for _, c := range s.Field {
			if c.IsPlayer || c.InPits || c.PitState != 0 {
				continue
			}
			if c.SpeedKmh >= 15 {
				continue // moving traffic, however slow, is not a hazard
			}
			if c.Sector != player.Sector {
				continue
			}
			d := wrapAhead(player.LapDist, c.LapDist, s.TrackLength)
			if d <= 0 || d > 500 {
				continue
			}
			prev, ok := t.snapDist[c.ID]
			if !ok {
				continue
			}
			moved := wrapAhead(prev, c.LapDist, s.TrackLength)
			if moved < 3 && playerMoved > 15 {
				if hazardDist < 0 || d < hazardDist {
					hazardDist = d
				}
			}
		}
	}
	// BLUE candidate: a strictly-faster-class car closing from
	// behind, detected on data the timing tower already proves in
	// the field — the CarInfo.Class strings (ranked by the tower's
	// classOrder) and the lap-distance geometry. The game's own
	// blue byte (player.Flag == 6) also qualifies on its own, but
	// the leg no longer DEPENDS on it: that byte's population in
	// LMU is unverified, and between it and the old evaluation
	// order (any yellow returned first), lapping Hypercars never
	// raised a pill in Ari's preview2 race.
	blueDist := -1.0
	blueClass := ""
	if player != nil {
		for _, c := range s.Field {
			if c.IsPlayer || c.InPits {
				continue
			}
			if !fasterClass(c.Class, player.Class) {
				continue // same or slower class: not a blue-flag car
			}
			d := wrapAhead(c.LapDist, player.LapDist, s.TrackLength) // distance behind
			if d <= 0 || d > 400 {
				continue
			}
			prev, ok := t.snapDist[c.ID]
			if !ok {
				continue
			}
			prevD := wrapAhead(prev, t.snapPlayer, s.TrackLength)
			if prevD-d > 1 { // gap is shrinking
				if blueDist < 0 || d < blueDist {
					blueDist, blueClass = d, c.Class
				}
			}
		}
	}
	blueActive := blueDist >= 0 || (player != nil && player.Flag == 6)

	// Priority (preview4): the near-stopped hazard ahead outranks
	// blue — it is the one that can hurt. There is no third tier
	// anymore: the game's sector yellow is not a pill source (see
	// the hazard note above for the log evidence that convicted it).
	if hazardDist >= 0 {
		text := "YELLOW"
		if ds, ok := sectorDisplay(player.Sector); ok {
			text = fmt.Sprintf("YELLOW S%d", ds)
		}
		text += fmt.Sprintf(" · CAR %.0fm", hazardDist)
		return FlagPill{Kind: "yellow", Text: text, Source: fmt.Sprintf("hazard %.0fm", hazardDist)}, true
	}
	if blueActive {
		text := "BLUE"
		src := "game"
		if blueDist >= 0 {
			if blueClass != "" {
				text += " · " + blueClass
			}
			text += fmt.Sprintf(" %.0fm", blueDist)
			src = "closing"
		}
		return FlagPill{Kind: "blue", Text: text, Source: src}, true
	}
	return FlagPill{}, false
}

// sectorDisplay maps a raw scoring mSector value to the sector
// number a driver calls it by. The header is blunt about the raw
// encoding: "0=sector3, 1=sector1, 2=sector2 (don't ask why)".
// ok=false for values outside the known set, in which case the
// pill text omits the sector rather than guessing.
func sectorDisplay(raw int) (int, bool) {
	switch raw {
	case 0:
		return 3, true
	case 1:
		return 1, true
	case 2:
		return 2, true
	}
	return 0, false
}
