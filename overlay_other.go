//go:build !windows

package main

// The overlay only exists on Windows; this stub keeps the package building
// (and its tests running) everywhere else.
func overlayLoop() {}
