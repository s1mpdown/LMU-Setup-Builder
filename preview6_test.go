package main

import (
	"strings"
	"testing"
)

// Preview6: the tower's session-context block. Ari races offline and
// the tower showed no time left, no session type, no event identity.
// The feed carries the first two (mEndET-mCurrentET, mSession) plus
// the track name; it publishes NO event name anywhere, so the header
// shows the track — never an invented event title.

func TestSessionLabel(t *testing.T) {
	// Ranges per the rF2/LMU header comment on mSession:
	// 0=testday, 1-4=practice, 5-8=qual, 9=warmup, 10-13=race.
	cases := map[int]string{
		0:  "TEST DAY",
		1:  "PRACTICE",
		4:  "PRACTICE",
		5:  "QUALIFYING",
		8:  "QUALIFYING",
		9:  "WARMUP",
		10: "RACE",
		13: "RACE",
		14: "SESSION", // outside every documented range
		99: "SESSION",
		-1: "SESSION",
	}
	for v, want := range cases {
		if got := sessionLabel(v); got != want {
			t.Errorf("sessionLabel(%d) = %q, want %q", v, got, want)
		}
	}
}

func TestFmtTimeLeft(t *testing.T) {
	cases := map[float64]string{
		0:    "0:00",
		59:   "0:59",
		61:   "1:01",
		599:  "9:59",
		2492: "41:32",
		3599: "59:59",
		3600: "1:00:00",
		3661: "1:01:01",
	}
	for sec, want := range cases {
		if got := fmtTimeLeft(sec); got != want {
			t.Errorf("fmtTimeLeft(%v) = %q, want %q", sec, got, want)
		}
	}
}

func TestTowerContextLines(t *testing.T) {
	// Timed race with a named track: both lines, time counting down.
	got := towerContextLines(Sample{
		TrackName:   "Daytona International Speedway Road Course",
		Session:     13,
		SessionLeft: 2492,
	})
	want := []string{"Daytona International Speedway Road Course", "RACE · 41:32 LEFT"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("timed race context = %v, want %v", got, want)
	}

	// Lap-counted session (EndET <= CurrentET surfaces as
	// SessionLeft = -1): the label stands alone, never a bogus 0:00.
	got = towerContextLines(Sample{TrackName: "Daytona", Session: 10, SessionLeft: -1})
	want = []string{"Daytona", "RACE"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("lap-counted context = %v, want %v", got, want)
	}
	got = towerContextLines(Sample{TrackName: "Daytona", Session: 10, SessionLeft: 0})
	if len(got) != 2 || got[1] != "RACE" {
		t.Errorf("zero-left context = %v, want label alone", got)
	}

	// No track name from the feed: the session line is the only line.
	got = towerContextLines(Sample{Session: 2, SessionLeft: 600})
	want = []string{"PRACTICE · 10:00 LEFT"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("trackless context = %v, want %v", got, want)
	}

	// An over-long track name truncates cleanly with an ellipsis.
	long := strings.Repeat("A", 70)
	got = towerContextLines(Sample{TrackName: long, Session: 9, SessionLeft: -1})
	if len(got) != 2 || len([]rune(got[0])) != 60 || !strings.HasSuffix(got[0], "…") {
		t.Errorf("truncated context = %v, want 60-rune first line ending in …", got)
	}
	if got[1] != "WARMUP" {
		t.Errorf("truncated context session line = %q, want WARMUP", got[1])
	}
}

func TestParseSampleSessionContext(t *testing.T) {
	tel := make([]byte, tVehicles+128*tVehStride)
	sco := make([]byte, sVehicles+128*sVehStride)
	info := sco[sInfo:]
	putStr(info, siTrackName, "Daytona")
	putI32(info, siSession, 13)
	putF64(info, siCurrentET, 600)
	putF64(info, siEndET, 3092)

	s := parseSample(tel, sco)
	if s.Session != 13 {
		t.Errorf("Session = %d, want 13", s.Session)
	}
	if s.SessionLeft != 2492 {
		t.Errorf("SessionLeft = %v, want 2492 (EndET-CurrentET)", s.SessionLeft)
	}

	// Lap-counted: EndET does not exceed CurrentET -> no time left.
	putF64(info, siEndET, 0)
	s = parseSample(tel, sco)
	if s.SessionLeft != -1 {
		t.Errorf("SessionLeft = %v, want -1 for a lap-counted session", s.SessionLeft)
	}
}
