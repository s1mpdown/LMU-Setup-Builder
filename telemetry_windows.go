//go:build windows

package main

// telemetry_windows.go — the Windows side of the telemetry logger:
// opens the game's shared-memory buffers, samples the player's car,
// records CSV laps files, and installs the feed plugin into the game.

import (
	"bufio"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

//go:embed assets/rFactor2SharedMemoryMapPlugin64.dll
var pluginDLL []byte

var (
	kernel32        = syscall.NewLazyDLL("kernel32.dll")
	procOpenFileMap = kernel32.NewProc("OpenFileMappingW")
	procMapView     = kernel32.NewProc("MapViewOfFile")
	procUnmapView   = kernel32.NewProc("UnmapViewOfFile")
	procCloseHandle = kernel32.NewProc("CloseHandle")
)

const fileMapRead = 0x0004

type mapping struct {
	handle syscall.Handle
	ptr    uintptr
	size   int
}

func openMapping(name string, size int) *mapping {
	namePtr, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return nil
	}
	h, _, _ := procOpenFileMap.Call(fileMapRead, 0, uintptr(unsafe.Pointer(namePtr)))
	if h == 0 {
		return nil
	}
	p, _, _ := procMapView.Call(h, fileMapRead, 0, 0, uintptr(size))
	if p == 0 {
		procCloseHandle.Call(h)
		return nil
	}
	return &mapping{handle: syscall.Handle(h), ptr: p, size: size}
}

func (m *mapping) bytes() []byte {
	return unsafe.Slice((*byte)(unsafe.Pointer(m.ptr)), m.size)
}

func (m *mapping) close() {
	if m.ptr != 0 {
		procUnmapView.Call(m.ptr)
	}
	if m.handle != 0 {
		procCloseHandle.Call(uintptr(m.handle))
	}
}

// ---- shared logger state ----

var tele struct {
	mu        sync.Mutex
	latest    Sample
	feedLive  bool
	recording bool
	recName   string
	recRows   int
}

var (
	recMu   sync.Mutex
	recFile *os.File
	recBuf  *bufio.Writer
)

func teleSnapshot() (Sample, bool, bool, string, int) {
	tele.mu.Lock()
	defer tele.mu.Unlock()
	return tele.latest, tele.feedLive, tele.recording, tele.recName, tele.recRows
}

func teleDir() string {
	if teleFolder != "" {
		return teleFolder
	}
	return filepath.Join(configDir(), "telemetry")
}

// Auto-record state (v1.3): recording starts by itself when the car
// is rolling on track and stops in the garage. The record button is a
// manual override — a manual stop suppresses auto-start until the car
// next sits in the garage, so his "stop" always sticks for the stint.
var (
	recAutoStarted bool
	recSuppress    bool
	garageSince    time.Time
	learner        outlineLearner
)

// teleToggleRecord is the record button: manual start/stop.
func teleToggleRecord() string {
	_, _, recording, _, _ := teleSnapshot()
	if recording {
		recSuppress = true
		recAutoStarted = false
		return teleSetRecording(false)
	}
	recSuppress = false
	recAutoStarted = false
	return teleSetRecording(true)
}

// teleAutoRecord drives the automatic start/stop. Called every loop
// pass with the freshest sample and feed state.
func teleAutoRecord(s Sample, live bool) {
	_, _, recording, _, _ := teleSnapshot()
	if !live {
		if recording && recAutoStarted {
			recAutoStarted = false
			teleSetRecording(false)
		}
		garageSince = time.Time{}
		return
	}
	inGarage := s.InPits && s.SpeedKmh < 1
	rolling := !s.InPits && s.SpeedKmh > 5
	if inGarage {
		if garageSince.IsZero() {
			garageSince = time.Now()
		}
		recSuppress = false // back in the garage: next stint may record
		if recording && recAutoStarted && time.Since(garageSince) > 3*time.Second {
			recAutoStarted = false
			teleSetRecording(false)
		}
		return
	}
	garageSince = time.Time{}
	if rolling && !recording && !recSuppress {
		if msg := teleSetRecording(true); msg != "" {
			recAutoStarted = true
			logLine("telemetry: auto-record started")
		}
	}
}

// teleSetRecording starts or stops the CSV recording. Returns a status line.
func teleSetRecording(on bool) string {
	recMu.Lock()
	defer recMu.Unlock()
	tele.mu.Lock()
	defer tele.mu.Unlock()
	if on && recFile == nil {
		if err := os.MkdirAll(teleDir(), 0755); err != nil {
			return "Could not create the telemetry folder: " + err.Error()
		}
		name := "telemetry-" + time.Now().Format("20060102-150405") + ".csv"
		f, err := os.Create(filepath.Join(teleDir(), name))
		if err != nil {
			return "Could not create the log file: " + err.Error()
		}
		recFile = f
		recBuf = bufio.NewWriterSize(f, 64*1024)
		recBuf.WriteString(csvHeader)
		tele.recording = true
		tele.recName = name
		tele.recRows = 0
		return "Recording to " + name
	}
	if !on && recFile != nil {
		recBuf.Flush()
		recFile.Close()
		done := fmt.Sprintf("Saved %s (%d samples). Send me that file and I'll read your traces.",
			filepath.Join(teleDir(), tele.recName), tele.recRows)
		recFile = nil
		recBuf = nil
		tele.recording = false
		return done
	}
	return ""
}

// teleLoop runs forever: attach to the game feed when it appears, sample the
// player's car, and write rows at 50 Hz while recording.
// Pit-menu diagnostics: the engineer went silent once and nobody could
// tell whether the game simply wasn't publishing the pit menu or the
// app wasn't reading it. These counters make one screenshot (or the log
// file) answer that: map found or not, and the last menu line seen.
var pitMapOK atomic.Bool
var pitMapWarned bool
var pitLiveSince time.Time

var pitDiag struct {
	sync.Mutex
	seen    int
	lastCat string
	lastCh  string
	lastKey string
}

func pitNote(cat, choice string) {
	pitDiag.Lock()
	defer pitDiag.Unlock()
	pitDiag.seen++
	pitDiag.lastCat, pitDiag.lastCh = cat, choice
	if key := cat + "|" + choice; key != pitDiag.lastKey {
		pitDiag.lastKey = key
		logLine("pitmenu: " + cat + " > " + choice)
	}
}

func pitDiagText() string {
	if !pitMapOK.Load() {
		return "Pit menu feed: NOT FOUND"
	}
	pitDiag.Lock()
	defer pitDiag.Unlock()
	if pitDiag.seen == 0 {
		return "Pit menu: never seen yet"
	}
	return "Pit menu: " + pitDiag.lastCat + " > " + pitDiag.lastCh
}

func teleLoop() {
	var telM, scoM, pitM *mapping
	var lastRow time.Time
	var lastFlush time.Time
	for {
		if telM == nil {
			telM = openMapping("$rFactor2SMMP_Telemetry$", telemetryFileSize)
		}
		if scoM == nil {
			scoM = openMapping("$rFactor2SMMP_Scoring$", scoringFileSize)
		}
		if pitM == nil {
			pitM = openMapping("$rFactor2SMMP_PitInfo$", pitInfoFileSize)
		}
		if telM != nil && pitLiveSince.IsZero() {
			pitLiveSince = time.Now()
		}
		if pitM != nil && !pitMapOK.Load() {
			pitMapOK.Store(true)
			logLine("feed: PitInfo map opened")
		}
		if telM != nil && pitM == nil && !pitMapWarned && !pitLiveSince.IsZero() && time.Since(pitLiveSince) > 15*time.Second {
			pitMapWarned = true
			logLine("feed: PitInfo map NOT found - the engineer cannot see the pit menu")
		}
		if telM == nil || scoM == nil {
			tele.mu.Lock()
			tele.feedLive = false
			tele.mu.Unlock()
			teleAutoRecord(Sample{}, false)
			time.Sleep(2 * time.Second)
			continue
		}
		tb := telM.bytes()
		sb := scoM.bytes()
		begin := uint32(tb[0]) | uint32(tb[1])<<8 | uint32(tb[2])<<16 | uint32(tb[3])<<24
		s := parseSample(tb[smVersionBlockSize:], sb[smVersionBlockSize:])
		end := uint32(tb[4]) | uint32(tb[5])<<8 | uint32(tb[6])<<16 | uint32(tb[7])<<24
		if begin != end || !s.OK {
			time.Sleep(2 * time.Millisecond)
			continue
		}
		now := time.Now()
		tele.mu.Lock()
		if pitM != nil {
			if pb := pitM.bytes(); len(pb) > smVersionBlockSize {
				if cat, choice, ok := parsePitMenu(pb[smVersionBlockSize:]); ok {
					s.MenuCat, s.MenuChoice = cat, choice
					if cat != "" {
						pitNote(cat, choice)
					}
				}
			}
		}
		tele.latest = s
		tele.feedLive = true
		histPush(&s)
		eng.Tick(s)
		stints.update(s.Field)
		if o := learner.Observe(s); o != nil {
			dir := filepath.Join(configDir(), "tracks")
			if err := saveOutline(dir, o); err == nil {
				logLine("track outline learned and stored: " + o.Track)
			} else {
				logLine("track outline save failed: " + err.Error())
			}
		}
		rec := tele.recording
		tele.mu.Unlock()
		teleAutoRecord(s, true)

		if rec && now.Sub(lastRow) >= 20*time.Millisecond {
			lastRow = now
			recMu.Lock()
			if recBuf != nil {
				recBuf.WriteString(csvRow(s))
				tele.mu.Lock()
				tele.recRows++
				tele.mu.Unlock()
				if now.Sub(lastFlush) >= time.Second {
					lastFlush = now
					recBuf.Flush()
				}
			}
			recMu.Unlock()
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// ---- plugin installation ----

// teleInstall copies the feed plugin into the game and enables it.
// Returns human-readable result lines. Never overwrites a different
// existing plugin file, and backs up the plugin settings JSON first.
func teleInstall(lmuPath string) []string {
	if lmuPath == "" || !looksLikeLMU(lmuPath) {
		return []string{"First point the app at your Le Mans Ultimate folder (it is needed for this too)."}
	}
	var out []string
	dllOK := false
	for _, sub := range []string{"Plugins", filepath.Join("Bin64", "Plugins")} {
		dir := filepath.Join(lmuPath, sub)
		dst := filepath.Join(dir, pluginDLLName)
		if data, err := os.ReadFile(dst); err == nil {
			if len(data) == len(pluginDLL) {
				out = append(out, "Feed plugin already in "+sub+" — leaving it as is.")
				dllOK = true
			} else {
				out = append(out, "A plugin file already exists in "+sub+" (a different copy of the same plugin) — leaving it untouched.")
				dllOK = true
			}
			continue
		}
		if err := os.MkdirAll(dir, 0755); err != nil {
			out = append(out, "Could not create "+sub+" in the game folder: "+err.Error())
			continue
		}
		if err := os.WriteFile(dst, pluginDLL, 0644); err != nil {
			out = append(out, "Could not copy the feed plugin into "+sub+": "+err.Error())
			continue
		}
		out = append(out, "Feed plugin installed into "+sub+".")
		dllOK = true
	}
	if !dllOK {
		return out
	}
	jsonPath := filepath.Join(lmuPath, "UserData", "player", "CustomPluginVariables.JSON")
	raw, err := os.ReadFile(jsonPath)
	if err != nil {
		out = append(out, "One step is left for the game itself: start Le Mans Ultimate once, drive out of the pits, quit the game, then press Install again — the game creates its plugin settings file on that first run, and I'll switch the feed on.")
		return out
	}
	backupDir := filepath.Join(lmuPath, "UserData", "player", "JettBackups")
	os.MkdirAll(backupDir, 0755)
	os.WriteFile(filepath.Join(backupDir, "CustomPluginVariables-"+time.Now().Format("20060102-150405")+".JSON"), raw, 0644)
	newText, changed := ensurePluginEnabled(string(raw))
	if !changed && strings.Contains(string(raw), pluginDLLName) {
		out = append(out, "Feed already switched on in the game's plugin settings.")
	} else if changed {
		if err := os.WriteFile(jsonPath, []byte(newText), 0644); err != nil {
			out = append(out, "Could not update the game's plugin settings: "+err.Error())
		} else {
			out = append(out, "Feed switched on in the game's plugin settings (backup saved in UserData\\player\\JettBackups).")
		}
	} else {
		out = append(out, "Could not find where to enable the feed in the game's plugin settings — send me a photo of this and I'll sort it.")
	}
	out = append(out, "Done. Launch the game, and the live numbers below should wake up in the pits.")
	return out
}
