package main

// preview4_test.go — tests for the v1.3.0-preview4 round's
// session-aware overlay visibility. (The accent-line removal is a
// pure drawing change with no logic to test; the game-sector-flag
// regression test for the flag change lives in preview3_test.go.)

import (
	"testing"
	"time"
)

// The in-session predicate: an OR of the feed's robust signals.
func TestSampleInSession(t *testing.T) {
	player := CarInfo{ID: 1, Driver: "YOU", IsPlayer: true}
	other := CarInfo{ID: 2, Driver: "RIVAL"}
	cases := []struct {
		name string
		s    Sample
		want bool
	}{
		{"menus: zero sample", Sample{}, false},
		{"invalid sample with signals", Sample{OK: false, GamePhase: 5, SessionET: 42, Field: []CarInfo{player}}, false},
		{"phase only: green", Sample{OK: true, GamePhase: 5}, true},
		{"phase only: countdown", Sample{OK: true, GamePhase: 4}, true},
		{"phase only: formation", Sample{OK: true, GamePhase: 3}, true},
		{"phase only: session stopped", Sample{OK: true, GamePhase: 7}, true},
		{"phase 0 = before session", Sample{OK: true, GamePhase: 0}, false},
		{"phase 8 = session over", Sample{OK: true, GamePhase: 8}, false},
		{"phase 9 = paused heartbeat", Sample{OK: true, GamePhase: 9}, false},
		{"player in field only", Sample{OK: true, Field: []CarInfo{other, player}}, true},
		{"field without player", Sample{OK: true, Field: []CarInfo{other}}, false},
		{"session clock only", Sample{OK: true, SessionET: 12.5}, true},
		{"session clock at zero", Sample{OK: true, SessionET: 0}, false},
		{"full session sample", Sample{OK: true, GamePhase: 5, SessionET: 300, Field: []CarInfo{player}}, true},
	}
	for _, c := range cases {
		if got := sampleInSession(c.s); got != c.want {
			t.Errorf("%s: sampleInSession = %v, want %v", c.name, got, c.want)
		}
	}
}

// The hysteresis: ~1 s of in-session signal to appear, ~10 s of
// continuous absence to hide, transitions reported exactly once.
func TestSessionGateTiming(t *testing.T) {
	g := &sessionGate{}
	t0 := time.Unix(1000, 0)
	at := func(sec float64) time.Time { return t0.Add(time.Duration(sec * float64(time.Second))) }

	// Out of session: hidden, no events.
	if v, j, l := g.update(false, at(0)); v || j || l {
		t.Fatalf("idle out: got v=%v j=%v l=%v", v, j, l)
	}
	// Join: signal arrives but the show delay has not elapsed.
	if v, j, _ := g.update(true, at(0.2)); v || j {
		t.Fatalf("join at +0.2s: got v=%v j=%v, want still hidden", v, j)
	}
	if v, j, _ := g.update(true, at(0.9)); v || j {
		t.Fatalf("join at +0.9s: got v=%v j=%v, want still hidden", v, j)
	}
	// Past ~1 s of continuous in-session: visible, joined fires.
	if v, j, l := g.update(true, at(1.3)); !v || !j || l {
		t.Fatalf("join at +1.3s: got v=%v j=%v l=%v, want visible+joined", v, j, l)
	}
	// Steady state: no repeat events.
	if v, j, l := g.update(true, at(5)); !v || j || l {
		t.Fatalf("steady in: got v=%v j=%v l=%v", v, j, l)
	}
	// A brief dropout (loading screen / feed hiccup) must NOT hide.
	if v, _, l := g.update(false, at(6)); !v || l {
		t.Fatalf("dropout start: got v=%v l=%v, want still visible", v, l)
	}
	if v, _, l := g.update(false, at(12)); !v || l {
		t.Fatalf("dropout 6s: got v=%v l=%v, want still visible", v, l)
	}
	// Signal back before 10 s: visible throughout, no new events,
	// and the absence clock resets.
	if v, j, l := g.update(true, at(13)); !v || j || l {
		t.Fatalf("return after dropout: got v=%v j=%v l=%v", v, j, l)
	}
	if v, _, l := g.update(false, at(14)); !v || l {
		t.Fatalf("second absence start: got v=%v l=%v", v, l)
	}
	if v, _, l := g.update(false, at(23.9)); !v || l {
		t.Fatalf("absence 9.9s: got v=%v l=%v, want still visible", v, l)
	}
	// ~10 s of continuous absence: hide, left fires once.
	if v, _, l := g.update(false, at(24.2)); v || !l {
		t.Fatalf("absence 10.2s: got v=%v l=%v, want hidden+left", v, l)
	}
	if v, _, l := g.update(false, at(30)); v || l {
		t.Fatalf("steady out: got v=%v l=%v, want silent", v, l)
	}
	// A sub-second in-session blip (menu flicker) never shows.
	if v, j, _ := g.update(true, at(31)); v || j {
		t.Fatalf("blip: got v=%v j=%v, want hidden", v, j)
	}
	if v, _, _ := g.update(false, at(31.5)); v {
		t.Fatal("blip end: gate should be hidden")
	}
	// A real re-join shows again after the show delay.
	g.update(true, at(40))
	if v, j, _ := g.update(true, at(41.2)); !v || !j {
		t.Fatalf("re-join: got v=%v j=%v, want visible+joined", v, j)
	}
}
