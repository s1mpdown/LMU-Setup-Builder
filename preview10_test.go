package main

// preview10_test.go — tests for the v1.3.0-preview10 session-gate
// freshness fix. Field defect (2026-11-02, after an official
// practice session): when the server ended and LMU returned to
// its menus, the overlay came with him and stayed — the day's log
// held multiple "session joined" lines and ZERO "session left".
// Mechanism: the shared-memory maps stay mapped with the last
// session's data frozen in place, the parse loop keeps succeeding
// on the leftovers, and the preview4 gate keyed off sample VALUES
// only (frozen phase in 1..7, frozen player entry, frozen
// SessionET > 0 — each enough on its own), never off freshness.
// The gate's reading now comes from sessionFeed: strong values
// count only while the session clock is provably advancing.

import (
	"testing"
	"time"
)

var (
	p10Player = CarInfo{ID: 1, Driver: "YOU", IsPlayer: true}
	p10Rival  = CarInfo{ID: 2, Driver: "RIVAL"}
)

// gateRig composes a sessionFeed and a sessionGate exactly the
// way the overlay loop does (feed reading -> gate input), polled
// on a fixed 100 ms tick with injected timestamps.
type gateRig struct {
	feed   *sessionFeed
	gate   *sessionGate
	t0     time.Time
	n      int // polls so far
	joined int
	left   int
}

func newGateRig() *gateRig {
	return &gateRig{feed: &sessionFeed{}, gate: &sessionGate{}, t0: time.Unix(2000, 0)}
}

// poll advances one 100 ms tick with sample s and returns the
// feed reading and the gate's visibility after it.
func (r *gateRig) poll(s Sample) (in, visible bool) {
	now := r.t0.Add(time.Duration(r.n) * 100 * time.Millisecond)
	in = r.feed.update(s, now)
	v, j, l := r.gate.update(in, now)
	r.n++
	if j {
		r.joined++
	}
	if l {
		r.left++
	}
	return in, v
}

// The strong predicate: phase and player legs count, the bare
// session clock does not.
func TestSessionSustain(t *testing.T) {
	cases := []struct {
		name string
		s    Sample
		want bool
	}{
		{"green phase", Sample{OK: true, GamePhase: 5}, true},
		{"session stopped phase", Sample{OK: true, GamePhase: 7}, true},
		{"player in field, no phase", Sample{OK: true, Field: []CarInfo{p10Rival, p10Player}}, true},
		{"player in field, phase over", Sample{OK: true, GamePhase: 8, Field: []CarInfo{p10Player}}, true},
		{"clock only", Sample{OK: true, SessionET: 500}, false},
		{"paused heartbeat, clock only", Sample{OK: true, GamePhase: 9, SessionET: 500}, false},
		{"rivals only, clock running", Sample{OK: true, SessionET: 500, Field: []CarInfo{p10Rival}}, false},
		{"menus: zero sample", Sample{}, false},
		{"invalid sample with signals", Sample{OK: false, GamePhase: 5, SessionET: 42, Field: []CarInfo{p10Player}}, false},
	}
	for _, c := range cases {
		if got := sessionSustain(c.s); got != c.want {
			t.Errorf("%s: sessionSustain = %v, want %v", c.name, got, c.want)
		}
		// sampleInSession keeps its preview4 meaning: the strong
		// legs OR the bare clock.
		wantIn := c.want || (c.s.OK && c.s.SessionET > 0)
		if got := sampleInSession(c.s); got != wantIn {
			t.Errorf("%s: sampleInSession = %v, want %v", c.name, got, wantIn)
		}
	}
}

// Frozen menu leftovers with the most insidious values — phase 7
// ("session stopped" is IN the 1..7 range) and the player still
// listed — must never join, from a cold start, however long they
// sit there. The clock never changes value, so the feed never
// calls it live.
func TestSessionFeedFrozenLeftoversNeverJoin(t *testing.T) {
	r := newGateRig()
	frozen := Sample{OK: true, GamePhase: 7, SessionET: 2871.4, Field: []CarInfo{p10Player, p10Rival}}
	for i := 0; i < 300; i++ { // 30 s of polls
		if in, vis := r.poll(frozen); in || vis {
			t.Fatalf("poll %d: frozen leftovers read in=%v visible=%v, want never", i, in, vis)
		}
	}
	if r.joined != 0 || r.left != 0 {
		t.Fatalf("joined=%d left=%d, want 0/0", r.joined, r.left)
	}
}

// Pure menu residue: clock frozen above zero, no field, phase 0.
func TestSessionFeedMenuResidueNeverJoins(t *testing.T) {
	r := newGateRig()
	menu := Sample{OK: true, GamePhase: 0, SessionET: 1234.5}
	for i := 0; i < 300; i++ {
		if in, vis := r.poll(menu); in || vis {
			t.Fatalf("poll %d: menu residue read in=%v visible=%v, want never", i, in, vis)
		}
	}
}

// A LIVE clock alone is not a session: with the clock advancing
// every poll but no player in the field and no active phase, the
// gate must never join. (Preview4's bare SessionET > 0 leg could
// sustain on exactly this.)
func TestSessionFeedClockAloneNeverJoins(t *testing.T) {
	r := newGateRig()
	for i := 0; i < 300; i++ {
		s := Sample{OK: true, GamePhase: 0, SessionET: 100 + 0.1*float64(i), Field: []CarInfo{p10Rival}}
		if in, vis := r.poll(s); in || vis {
			t.Fatalf("poll %d: live clock alone read in=%v visible=%v, want never", i, in, vis)
		}
	}
}

// The on-server garage screen: the player is entered in the
// session (player leg alone, no active phase) and the session
// clock keeps running. The gate joins in ~1 s and stays joined
// for minutes.
func TestSessionFeedLiveGarageStaysJoined(t *testing.T) {
	r := newGateRig()
	visibleAt := -1
	for i := 0; i < 1200; i++ { // 120 s of polls
		s := Sample{OK: true, GamePhase: 0, SessionET: 600 + 0.1*float64(i), Field: []CarInfo{p10Player, p10Rival}}
		_, vis := r.poll(s)
		if vis && visibleAt < 0 {
			visibleAt = i
		}
		if visibleAt >= 0 && !vis {
			t.Fatalf("poll %d: garage overlay hid after joining at poll %d", i, visibleAt)
		}
	}
	if visibleAt < 0 || visibleAt > 15 {
		t.Fatalf("joined at poll %d, want within ~1.5 s of the first poll", visibleAt)
	}
	if r.joined != 1 || r.left != 0 {
		t.Fatalf("joined=%d left=%d, want 1/0", r.joined, r.left)
	}
}

// The field defect itself: a live session joins, then the
// session ends and the data freezes mid-values. Freshness lapses
// 3 s after the clock stops; the gate's 10 s absence timer then
// hides it — gone within ~13 s of the freeze, with exactly one
// "session left".
func TestSessionFeedFreezeAfterJoinLeavesBounded(t *testing.T) {
	r := newGateRig()
	// Live: 5 s of advancing clock, green phase, player present.
	for i := 0; i <= 50; i++ {
		r.poll(Sample{OK: true, GamePhase: 5, SessionET: 100 + 0.1*float64(i), Field: []CarInfo{p10Player}})
	}
	if r.joined != 1 {
		t.Fatalf("live session: joined=%d, want 1", r.joined)
	}
	// Frozen from poll 51 on: same strong values, clock stuck at
	// its last value (105.0, last seen moving at poll 50, t=5.0).
	frozen := Sample{OK: true, GamePhase: 5, SessionET: 105.0, Field: []CarInfo{p10Player}}
	hiddenAt := -1
	for i := 51; i <= 200; i++ {
		in, vis := r.poll(frozen)
		switch i {
		case 80: // t=8.0: 3.0 s since the clock moved — still fresh
			if !in {
				t.Fatal("poll 80: feed dropped exactly at the freshness boundary, want still live")
			}
		case 81: // t=8.1: clock provably frozen — absence starts
			if in {
				t.Fatal("poll 81: feed still live 3.1 s after the clock froze")
			}
		}
		if !vis && hiddenAt < 0 {
			hiddenAt = i
		}
	}
	if hiddenAt < 0 {
		t.Fatal("overlay never hid on frozen data")
	}
	// Freeze began at t=5.0; hidden must land at ~t=18.1
	// (3 s freshness + 10 s absence + one poll of slack).
	if hiddenAt > 185 {
		t.Fatalf("hidden at poll %d (t=%.1fs), want by poll 185 — menu linger is unbounded", hiddenAt, float64(hiddenAt)/10)
	}
	if r.left != 1 || r.joined != 1 {
		t.Fatalf("joined=%d left=%d, want 1/1", r.joined, r.left)
	}
}

// Offline pause: the clock freezes while paused (heartbeat
// phase, player still listed), so the overlay hides after the
// same bounded time; unpausing restarts the clock and the gate
// rejoins in ~1 s.
func TestSessionFeedPauseHidesAndResumeRejoins(t *testing.T) {
	r := newGateRig()
	for i := 0; i <= 50; i++ {
		r.poll(Sample{OK: true, GamePhase: 5, SessionET: 100 + 0.1*float64(i), Field: []CarInfo{p10Player}})
	}
	paused := Sample{OK: true, GamePhase: 9, SessionET: 105.0, Field: []CarInfo{p10Player}}
	hidden := false
	for i := 51; i <= 195; i++ {
		_, vis := r.poll(paused)
		if !vis {
			hidden = true
		}
	}
	if !hidden || r.left != 1 {
		t.Fatalf("pause: hidden=%v left=%d, want hidden with one left", hidden, r.left)
	}
	// Resume: the clock advances again from where it froze.
	rejoinedAt := -1
	for i := 196; i <= 215; i++ {
		s := Sample{OK: true, GamePhase: 5, SessionET: 105 + 0.1*float64(i-195), Field: []CarInfo{p10Player}}
		_, vis := r.poll(s)
		if vis && rejoinedAt < 0 {
			rejoinedAt = i
		}
	}
	if rejoinedAt < 0 || rejoinedAt > 196+12 {
		t.Fatalf("rejoined at poll %d, want within ~1.2 s of the clock resuming", rejoinedAt)
	}
	if r.joined != 2 || r.left != 1 {
		t.Fatalf("joined=%d left=%d, want 2/1", r.joined, r.left)
	}
}
