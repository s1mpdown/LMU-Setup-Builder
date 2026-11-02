package main

// switchbg.go — the colour a PANELS-row toggle switch paints behind
// its pill (preview9).
//
// The switches live on the OVERLAY tab page, whose background is the
// KAIGO navy --bg (0x0d1321). drawSwitch (main.go, windows) fills the
// switch's full bounds with this colour before drawing track and
// knob: walk erases a PaintNormal CustomWidget with the window-class
// brush (COLOR_BTNFACE, light grey) because neither the switch nor
// its row composite carries a Background brush for walk's erase
// handler to use, and the pill alone never covered the erased
// margins — leaving a light-grey block around every switch. Kept as
// a pure helper so the exact value is pinned by a unit test
// (preview9_test.go); do not "correct" it to the header's surface
// colour (0x111d33) — the PANELS row sits on the page, not the header.

// switchPageBackgroundRGB returns the OVERLAY page background as
// separate R, G, B components (walk.RGB is windows-only, so the value
// lives here in platform-neutral form).
func switchPageBackgroundRGB() (uint8, uint8, uint8) {
	return 0x0d, 0x13, 0x21
}
