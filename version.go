package main

// AppVersion is this build's version. CI stamps it from the repo's
// VERSION file via -ldflags "-X main.AppVersion=x.y.z"; the default here
// is what a local build reports.
var AppVersion = "1.2.1"

// updateRepo is where the app checks for new builds. It is a public
// repo, so the check and the download need no login.
const updateRepo = "s1mpdown/LMU-Setup-Builder"

// updateAssetName is the exe file attached to each GitHub release.
const updateAssetName = "LMU-Setup-Builder.exe"
