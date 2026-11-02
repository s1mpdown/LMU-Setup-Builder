package main

import "testing"

// Preview7: the REVIEW plots must fill the whole plot area as a
// vertical stack (map dominant on top, delta middle, traces
// bottom) at any canvas size — the old fixed column/strip rects
// left most of a tall canvas empty.

func TestRevStackFillsArea(t *testing.T) {
	for _, wh := range [][2]int{{900, 400}, {1256, 640}, {1256, 700}, {640, 480}, {1920, 900}} {
		w, h := wh[0], wh[1]
		m, d, tr := revStackRects(w, h)
		// Full inner width for every section.
		for name, r := range map[string]revRect{"map": m, "delta": d, "trace": tr} {
			if r.X != revPad || r.W != w-2*revPad {
				t.Errorf("%dx%d %s: X=%d W=%d, want X=%d W=%d", w, h, name, r.X, r.W, revPad, w-2*revPad)
			}
			if r.H < 0 {
				t.Errorf("%dx%d %s: negative height %d", w, h, name, r.H)
			}
		}
		// Stacked in order, no overlap: map above delta above traces.
		if m.Y != revPad {
			t.Errorf("%dx%d map Y=%d, want %d", w, h, m.Y, revPad)
		}
		if d.Y != m.Y+m.H+revGap {
			t.Errorf("%dx%d delta Y=%d, want %d", w, h, d.Y, m.Y+m.H+revGap)
		}
		if tr.Y != d.Y+d.H+revGap {
			t.Errorf("%dx%d trace Y=%d, want %d", w, h, tr.Y, d.Y+d.H+revGap)
		}
		// The stack ends flush with the bottom margin — no dead space.
		if got := tr.Y + tr.H; got != h-revPad {
			t.Errorf("%dx%d stack bottom=%d, want %d", w, h, got, h-revPad)
		}
		// Map is the dominant section: at least as tall as the
		// other two combined (it gets ~half the height).
		if m.H < d.H+tr.H-1 || m.H > d.H+tr.H+1 {
			t.Errorf("%dx%d map H=%d vs delta+trace=%d, want ~equal (map ≈ half)", w, h, m.H, d.H+tr.H)
		}
	}
}

func TestRevStackDegenerate(t *testing.T) {
	for _, wh := range [][2]int{{0, 0}, {10, 10}, {16, 32}, {100, 20}} {
		m, d, tr := revStackRects(wh[0], wh[1])
		for name, r := range map[string]revRect{"map": m, "delta": d, "trace": tr} {
			if r.W < 0 || r.H < 0 {
				t.Errorf("%dx%d %s: negative size %+v", wh[0], wh[1], name, r)
			}
		}
		if d.Y < m.Y+m.H || tr.Y < d.Y+d.H {
			t.Errorf("%dx%d sections overlap: %+v %+v %+v", wh[0], wh[1], m, d, tr)
		}
	}
}
