package main

import "testing"

// Preview9: the switch bounds fill must match the OVERLAY page
// background (KAIGO --bg navy) exactly — any other value re-creates a
// visible block around the pill, just in a different colour.
func TestSwitchPageBackgroundIsPageNavy(t *testing.T) {
	r, g, b := switchPageBackgroundRGB()
	if r != 0x0d || g != 0x13 || b != 0x21 {
		t.Fatalf("switch page background = #%02x%02x%02x, want #0d1321 (OVERLAY page navy)", r, g, b)
	}
}
