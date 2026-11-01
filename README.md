# LMU Setup Builder — Powered by Jett

A personal setup + telemetry tool for **Le Mans Ultimate** (LMGT3):

- One-click setup generation for all 10 LMGT3 cars (qualifying / 20 / 30 / 60-minute race plans, fuel + Virtual Energy maths)
- Live telemetry recording (50 Hz CSV) through the rF2 shared-memory plugin — the app can install the plugin into the game for you
- A racing overlay: inputs trace, delta, relative, tyres, fuel — each panel individually toggleable and draggable
- A text race engineer: fuel-as-laps, pit-window calls, damage verdicts, and a pit-stop estimator that reprices your stop as you browse the in-game pit MFD

## Getting the app

Download `LMU-Setup-Builder.exe` from the [latest release](../../releases/latest) and run it. No installer.

**Settings are never reset by updates**: everything (panel positions, toggles, paths) lives in `%APPDATA%\LMU Setup Builder`.

## Updating

The app updates itself: click **UPDATE** in the top bar. It checks this repo's latest release; if a newer version exists, the button becomes **INSTALL**, downloads the new build, and restarts into it.

Releases are built automatically by GitHub Actions from this source whenever the `VERSION` file changes — the version stamped into the app always matches the release tag.

## Building from source

Requires Go 1.23+ on Windows. Two build assets are stored base64-encoded (the connector used to publish this repo is text-only); decode them first:

```bash
cat assets/plugin.dll.b64.part* | base64 -d > assets/rFactor2SharedMemoryMapPlugin64.dll
base64 -d app.ico.b64 > app.ico
go run github.com/akavel/rsrc@latest -ico app.ico -manifest app.manifest -o rsrc.syso
go build -ldflags "-H windowsgui -X main.AppVersion=$(cat VERSION)" -o LMU-Setup-Builder.exe .
```
