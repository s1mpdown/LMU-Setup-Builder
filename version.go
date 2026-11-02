package main

import "strconv"

// AppVersion is this build's version. CI stamps it from the repo's
// VERSION file via -ldflags "-X main.AppVersion=x.y.z"; the default here
// is what a local build reports.
var AppVersion = "1.3.0"

// versionMinor is the "x.y" of a version — the What's-New ack key.
// (Built on versionParts in update.go.)
func versionMinor(v string) string {
	p := versionParts(v)
	return strconv.Itoa(p[0]) + "." + strconv.Itoa(p[1])
}

// isFeatureRelease reports whether v is an x.y.0 release (the kind
// that shows a one-time What's New notice; patch bumps stay quiet).
func isFeatureRelease(v string) bool {
	return versionParts(v)[2] == 0
}

// updateRepo is where the app checks for new builds. It is a public
// repo, so the check and the download need no login.
const updateRepo = "s1mpdown/LMU-Setup-Builder"

// updateAssetName is the exe file attached to each GitHub release.
const updateAssetName = "LMU-Setup-Builder.exe"
