package main

// review_layout.go — the REVIEW tab's plot-stack geometry, kept
// platform-neutral so the arrangement math is unit-testable off
// Windows (review_windows.go only converts the results to
// walk.Rectangle). Preview7: the map used to sit in a fixed
// 476×330 column with the delta and pedal plots in fixed-height
// strips beside it, all top-left anchored — on a tall page most
// of the canvas was empty background below them. The three plots
// are now a vertical stack that fills the whole plot area: map
// on top (dominant, ~half the height), delta in the middle,
// pedal traces at the bottom, every section full width.

// revRect is a plain-int rectangle in canvas coordinates.
type revRect struct{ X, Y, W, H int }

const (
	revPad = 8 // margin around the stack, all sides
	revGap = 8 // vertical gap between sections
)

// revStackRects splits a w×h plot area into the three stacked
// section rectangles: map, delta, traces. The sections always
// span the full inner width, stack in that order with no
// overlap, and together consume the full inner height — the
// traces section absorbs any rounding remainder so the stack
// ends flush with the bottom margin. Degenerate inputs (zero
// or tiny areas) yield non-negative, non-overlapping rects.
func revStackRects(w, h int) (mapR, deltaR, traceR revRect) {
	iw := w - 2*revPad
	if iw < 0 {
		iw = 0
	}
	avail := h - 2*revPad - 2*revGap
	if avail < 0 {
		avail = 0
	}
	mapH := avail / 2
	deltaH := avail / 4
	traceH := avail - mapH - deltaH
	x := revPad
	mapR = revRect{X: x, Y: revPad, W: iw, H: mapH}
	deltaR = revRect{X: x, Y: revPad + mapH + revGap, W: iw, H: deltaH}
	traceR = revRect{X: x, Y: revPad + mapH + revGap + deltaH + revGap, W: iw, H: traceH}
	return mapR, deltaR, traceR
}
