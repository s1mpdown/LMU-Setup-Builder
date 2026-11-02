package main

// preview8_test.go — tests for the v1.3.0-preview8 round: the
// overlay arms itself at startup and SHOW/HIDE is a per-run
// override only.
//
// Ari's report on preview7: "i still need to hit show / hide
// overlay tho that thing not auto." Preview4's session gate
// automated the overlay WINDOW (shows ~1 s after joining, hides
// ~10 s after leaving), but only while the master SHOW/HIDE state
// was on — and a persisted off (an older version's default, or a
// HIDE he pressed in a previous run and the app then saved) left
// the overlay disarmed until he pressed SHOW OVERLAY by hand.
//
// The state logic lives in overlayInit / overlayConfig / the
// ovState.Shown atomic (the render loop composes it as
// Shown && sessionGate, in overlay_windows.go), so the rules are
// pinned here at that layer:
//   - startup arms regardless of the stored shown value;
//   - a manual hide holds for the current run;
//   - the manual hide is still written to the config (the key
//     stays for compatibility) but the next launch re-arms.

import "testing"

// Startup must arm the overlay whatever the config stored: the
// session gate decides when it appears, never a button.
func TestPreview8StartupArmsRegardlessOfStoredShown(t *testing.T) {
	for _, stored := range []bool{false, true} {
		overlayInit(stored, nil, nil, nil, 0)
		if !ovState.Shown.Load() {
			t.Errorf("stored shown=%v: overlay must be armed after startup", stored)
		}
	}
}

// The manual override: pressing HIDE (the button's whole effect is
// the Shown store in main.go's onOverlayToggle) suppresses the
// overlay for the rest of the run...
func TestPreview8ManualHideHoldsForTheRun(t *testing.T) {
	overlayInit(false, nil, nil, nil, 0) // armed at startup despite stored false
	if !ovState.Shown.Load() {
		t.Fatal("precondition: overlay should start armed")
	}
	ovState.Shown.Store(false) // SHOW/HIDE pressed: hide
	if ovState.Shown.Load() {
		t.Error("manual hide must take effect for the current run")
	}
	ovState.Shown.Store(true) // SHOW pressed again: override released
	if !ovState.Shown.Load() {
		t.Error("manual show must release the override within the run")
	}
}

// ...but it must not become a startup state. The app still writes
// the current value to the config (compatibility), yet feeding
// that stored false into the next launch re-arms the overlay.
func TestPreview8ManualHideDoesNotSurviveRestart(t *testing.T) {
	overlayInit(true, nil, nil, nil, 0)
	ovState.Shown.Store(false) // manual hide, then the app exits
	on, _, _, _, _ := overlayConfig()
	if on {
		t.Error("overlayConfig should still report the live state (shown key still written)")
	}
	overlayInit(on, nil, nil, nil, 0) // next launch reads the stored false
	if !ovState.Shown.Load() {
		t.Error("restart must re-arm the overlay even when the stored value is a manual hide")
	}
}
