package main

// telemetry_parse.go — reading the game's live telemetry feed.
// The game (via the standard rFactor2 shared-memory plugin) publishes two
// memory buffers: Telemetry (pedals, steering, speed for every car) and
// Scoring (lap distance, places, lap times). The byte offsets below come
// from the plugin's own rF2State.h (structures packed to 4 bytes), measured
// with a layout dump of that exact header — not guessed.
//
// This file is platform-neutral on purpose: the parser works on plain byte
// slices, so it is unit-tested on any OS. The Windows mapping code lives in
// telemetry_windows.go.

import (
	"encoding/binary"
	"fmt"
	"math"
	"sync"
	"strings"
)

// Each mapped file starts with an 8-byte version block (two counters the
// writer bumps before/after each update), then the payload struct.
const smVersionBlockSize = 8

// Telemetry payload offsets.
const (
	tNumVehicles = 4
	tVehicles    = 8
	tVehStride   = 1888

	vID          = 0
	vElapsedTime = 12
	vLapNumber   = 20
	vVehicleName = 32
	vTrackName   = 96
	vLocalVel    = 184 // rF2Vec3: x, y, z doubles
	vGear        = 352
	vEngineRPM   = 356
	vUnfThrottle = 388
	vUnfBrake    = 396
	vUnfSteering = 404
	vFilThrottle = 420
	vFilBrake    = 428
	vFuel        = 524
	vDetached    = 542
	vDentSeverity = 544
	vSector      = 600
	vWheels      = 848

	wheelStride  = 260
	wPressure    = 120
	wTemperature = 128 // three doubles across the tread; centre is +8
	wFlat        = 177
	wDetachedW   = 178
	wWear        = 152
)

// Scoring payload offsets.
const (
	sInfo       = 4
	siTrackName = 0
	siSession   = 64
	siCurrentET = 68
	siEndET     = 76
	siMaxLaps   = 84
	siLapDist   = 88 // track length in metres
	siNumVeh    = 104
	siGamePhase = 108
	siRaining   = 220
	siTrackTemp = 236

	sVehicles    = 552
	sVehStride   = 584
	svID         = 0
	svDriverName = 4
	svVehName    = 36
	svTotalLaps  = 100
	svSector     = 102
	svLapDist    = 104
	svBestLap    = 144
	svLastLap    = 168
	svPitstops   = 192
	svBehindLead = 244
	svIsPlayer   = 196
	svInPits     = 198
	svPitState   = 457
	svPlace      = 199
)

// Full mapped-file sizes (version block + payload).
const (
	telemetryFileSize = smVersionBlockSize + tVehicles + 128*tVehStride
	scoringFileSize   = smVersionBlockSize + sVehicles + 128*sVehStride
)

// Sample is one snapshot of the player's car.
type Sample struct {
	OK          bool
	ElapsedTime float64
	LapNumber   int
	LapDist     float64
	TrackLength float64
	TrackName   string
	VehicleName string
	SpeedKmh    float64
	Throttle    float64 // unfiltered (the driver's actual foot), 0..1
	Brake       float64
	Steering    float64 // -1..1
	ThrottleF   float64 // filtered (what the car applied)
	BrakeF      float64
	Gear        int
	RPM         float64
	Fuel        float64
	Sector      int
	InPits      bool
	Place       int
	LastLap     float64
	BestLap     float64
	SessionET   float64 // session clock now
	SessionLeft float64 // seconds left in a timed session; -1 when unknown
	MaxLaps     int
	Raining     float64 // 0..1 from the scoring header
	TrackTemp   float64
	PitState    int // 0 none, 1 requested, 2 entering, 3 stopped, 4 exiting
	PitStops    int
	Dents       [8]int // dent severity 0..2 at 8 points around the car
	PartsOff    bool   // any bodywork detached
	TyreFlat    [4]bool
	TyreOff     [4]bool
	MenuCat     string // pit MFD category currently shown ("" = no data)
	MenuChoice  string // pit MFD choice currently selected
	TyreWear    [4]float64 // fraction remaining, FL FR RL RR
	TyrePress   [4]float64 // kPa
	TyreTemp    [4]float64 // Celsius, centre of tread
	Field       []CarInfo  // every car in scoring order
}

// CarInfo is one scoring entry — the raw material for the relative panel.
type CarInfo struct {
	ID           int
	Driver       string
	Vehicle      string
	Place        int
	TotalLaps    int
	LapDist      float64
	BehindLeader float64 // seconds behind the leader
	IsPlayer     bool
	InPits       bool
	PitState     int
}

func sf64(b []byte, off int) float64 {
	if off+8 > len(b) {
		return 0
	}
	return math.Float64frombits(binary.LittleEndian.Uint64(b[off:]))
}

func si32(b []byte, off int) int {
	if off+4 > len(b) {
		return 0
	}
	return int(int32(binary.LittleEndian.Uint32(b[off:])))
}

func sstr(b []byte, off, max int) string {
	if off >= len(b) {
		return ""
	}
	end := off + max
	if end > len(b) {
		end = len(b)
	}
	s := b[off:end]
	if i := indexByte(s, 0); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(string(s))
}

func indexByte(b []byte, c byte) int {
	for i, x := range b {
		if x == c {
			return i
		}
	}
	return -1
}

func clampVehicles(n int) int {
	if n < 0 {
		return 0
	}
	if n > 128 {
		return 128
	}
	return n
}

// parseSample extracts the player's snapshot from the two payload buffers
// (payload = the bytes after the 8-byte version block).
func parseSample(tel, sco []byte) Sample {
	var s Sample
	if len(tel) < tVehicles || len(sco) < sVehicles {
		return s
	}
	nTel := clampVehicles(si32(tel, tNumVehicles))
	info := sco[sInfo:]
	nSco := clampVehicles(si32(info, siNumVeh))
	s.TrackLength = sf64(info, siLapDist)
	s.TrackName = sstr(info, siTrackName, 64)
	s.SessionET = sf64(info, siCurrentET)
	s.SessionLeft = -1
	if endET := sf64(info, siEndET); endET > s.SessionET {
		s.SessionLeft = endET - s.SessionET
	}
	s.MaxLaps = si32(info, siMaxLaps)
	s.Raining = sf64(info, siRaining)
	s.TrackTemp = sf64(info, siTrackTemp)

	// The whole field, for the relative panel.
	for i := 0; i < nSco; i++ {
		vb := sco[sVehicles+i*sVehStride:]
		if len(vb) < sVehStride {
			break
		}
		s.Field = append(s.Field, CarInfo{
			ID:           si32(vb, svID),
			Driver:       sstr(vb, svDriverName, 32),
			Vehicle:      sstr(vb, svVehName, 64),
			Place:        int(vb[svPlace]),
			TotalLaps:    int(int16(binary.LittleEndian.Uint16(vb[svTotalLaps:]))),
			LapDist:      sf64(vb, svLapDist),
			BehindLeader: sf64(vb, svBehindLead),
			IsPlayer:     vb[svIsPlayer] != 0,
			InPits:       vb[svInPits] != 0,
			PitState:     int(vb[svPitState]),
		})
	}

	// Find the player in scoring.
	playerID := -1
	playerIdx := -1
	for i := 0; i < nSco; i++ {
		vb := sco[sVehicles+i*sVehStride:]
		if len(vb) < sVehStride {
			break
		}
		if vb[svIsPlayer] != 0 {
			playerID = si32(vb, svID)
			playerIdx = i
			s.LapDist = sf64(vb, svLapDist)
			s.LastLap = sf64(vb, svLastLap)
			s.BestLap = sf64(vb, svBestLap)
			s.Place = int(vb[svPlace])
			s.InPits = vb[svInPits] != 0
			s.PitState = int(vb[svPitState])
			s.PitStops = int(int16(binary.LittleEndian.Uint16(vb[svPitstops:])))
			break
		}
	}

	// Find the same car in telemetry (fall back to the first car).
	tIdx := 0
	if playerID >= 0 {
		for i := 0; i < nTel; i++ {
			vb := tel[tVehicles+i*tVehStride:]
			if len(vb) >= 4 && si32(vb, vID) == playerID {
				tIdx = i
				break
			}
		}
	} else if nTel == 0 {
		return s
	}
	_ = playerIdx
	vb := tel[tVehicles+tIdx*tVehStride:]
	if len(vb) < tVehStride {
		return s
	}
	s.OK = true
	s.ElapsedTime = sf64(vb, vElapsedTime)
	s.LapNumber = si32(vb, vLapNumber)
	s.VehicleName = sstr(vb, vVehicleName, 64)
	if s.TrackName == "" {
		s.TrackName = sstr(vb, vTrackName, 64)
	}
	vx := sf64(vb, vLocalVel)
	vy := sf64(vb, vLocalVel+8)
	vz := sf64(vb, vLocalVel+16)
	s.SpeedKmh = math.Sqrt(vx*vx+vy*vy+vz*vz) * 3.6
	s.Throttle = sf64(vb, vUnfThrottle)
	s.Brake = sf64(vb, vUnfBrake)
	s.Steering = sf64(vb, vUnfSteering)
	s.ThrottleF = sf64(vb, vFilThrottle)
	s.BrakeF = sf64(vb, vFilBrake)
	s.Gear = si32(vb, vGear)
	s.RPM = sf64(vb, vEngineRPM)
	s.Fuel = sf64(vb, vFuel)
	s.Sector = si32(vb, vSector)
	s.PartsOff = vb[vDetached] != 0
	for i := 0; i < 8; i++ {
		s.Dents[i] = int(vb[vDentSeverity+i])
	}
	for w := 0; w < 4; w++ {
		wb := vb[vWheels+w*wheelStride:]
		if len(wb) < wheelStride {
			break
		}
		s.TyrePress[w] = sf64(wb, wPressure)
		s.TyreTemp[w] = sf64(wb, wTemperature+8)
		s.TyreWear[w] = sf64(wb, wWear)
		s.TyreFlat[w] = wb[wFlat] != 0
		s.TyreOff[w] = wb[wDetachedW] != 0
	}
	return s
}

const csvHeader = "et_s,lap,lap_dist_m,speed_kmh,throttle,brake,steering,gear,rpm,fuel_l\n"

func csvRow(s Sample) string {
	return fmt.Sprintf("%.3f,%d,%.1f,%.1f,%.4f,%.4f,%.4f,%d,%.0f,%.2f\n",
		s.ElapsedTime, s.LapNumber, s.LapDist, s.SpeedKmh,
		s.Throttle, s.Brake, s.Steering, s.Gear, s.RPM, s.Fuel)
}

// PitInfo buffer: the game's pit MFD state, published by the same plugin.
// Layout (payload after the 8-byte version block): rF2PitMenu —
// category index (0), category name (4, 32 chars), choice index (36),
// choice string (40, 32 chars), num choices (72). 332 bytes total.
const pitInfoFileSize = smVersionBlockSize + 332

func parsePitMenu(buf []byte) (cat, choice string, ok bool) {
	if len(buf) < 76 {
		return "", "", false
	}
	return sstr(buf, 4, 32), sstr(buf, 40, 32), true
}

// ---- CustomPluginVariables.JSON handling ----

const pluginDLLName = "rFactor2SharedMemoryMapPlugin64.dll"

const pluginJSONBlock = `  "` + pluginDLLName + `": {
    " Enabled": 1,
    "DebugISIInternals": 0,
    "DebugOutputLevel": 0,
    "DebugOutputSource": 0,
    "DedicatedServerMapGlobally": 0,
    "EnableDirectMemoryAccess": 0,
    "EnableHWControlInput": 0,
    "EnableRulesControlInput": 0,
    "EnableWeatherControlInput": 0,
    "UnsubscribedBuffersMask": 0
  }`

// ensurePluginEnabled returns the JSON text with the shared-memory plugin
// present and enabled, and whether anything changed. The game's file is a
// loose JSON dialect (" Enabled" keys carry a leading space), so this works
// by careful text surgery and never touches other plugins' entries.
func ensurePluginEnabled(text string) (string, bool) {
	key := `"` + pluginDLLName + `"`
	idx := strings.Index(text, key)
	if idx < 0 {
		// Insert the block just before the document's closing brace.
		close := strings.LastIndex(text, "}")
		if close < 0 {
			return text, false
		}
		// Is the object empty (only whitespace between braces)?
		open := strings.Index(text, "{")
		inner := ""
		if open >= 0 && open < close {
			inner = strings.TrimSpace(text[open+1 : close])
		}
		sep := ",\n"
		if inner == "" {
			sep = "\n"
		}
		return text[:close] + sep + pluginJSONBlock + "\n" + text[close:], true
	}
	// Entry exists: find its block and make sure " Enabled" is 1.
	blockStart := strings.Index(text[idx:], "{")
	if blockStart < 0 {
		return text, false
	}
	blockStart += idx
	depth := 0
	blockEnd := -1
	for i := blockStart; i < len(text); i++ {
		switch text[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				blockEnd = i
			}
		}
		if blockEnd >= 0 {
			break
		}
	}
	if blockEnd < 0 {
		return text, false
	}
	block := text[blockStart : blockEnd+1]
	enIdx := strings.Index(block, `" Enabled"`)
	if enIdx < 0 {
		newBlock := block[:1] + "\n    \" Enabled\": 1," + block[1:]
		return text[:blockStart] + newBlock + text[blockEnd+1:], true
	}
	// Find the value after the colon.
	colon := strings.Index(block[enIdx:], ":")
	if colon < 0 {
		return text, false
	}
	vStart := enIdx + colon + 1
	for vStart < len(block) && (block[vStart] == ' ' || block[vStart] == '\t') {
		vStart++
	}
	if vStart < len(block) && block[vStart] == '1' {
		return text, false // already enabled
	}
	if vStart < len(block) && block[vStart] >= '0' && block[vStart] <= '9' {
		newBlock := block[:vStart] + "1" + block[vStart+1:]
		return text[:blockStart] + newBlock + text[blockEnd+1:], true
	}
	return text, false
}

// TracePoint is one (distance, lap-elapsed) pair from a recorded lap.
type TracePoint struct {
	Dist    float64
	Elapsed float64
}

// DeltaAgainstBest returns how far ahead (+) or behind (-) the current lap
// is versus the best lap at the same distance, in seconds. ok=false when
// there is no usable reference yet.
func DeltaAgainstBest(best []TracePoint, curDist, curElapsed float64) (float64, bool) {
	if len(best) < 2 || curDist < 30 {
		return 0, false
	}
	// Binary search the best lap's elapsed at curDist.
	lo, hi := 0, len(best)-1
	if curDist > best[hi].Dist {
		return 0, false
	}
	for lo < hi {
		mid := (lo + hi) / 2
		if best[mid].Dist < curDist {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	i := lo
	if i == 0 {
		return 0, false
	}
	a, b := best[i-1], best[i]
	if b.Dist <= a.Dist {
		return 0, false
	}
	ref := a.Elapsed + (b.Elapsed-a.Elapsed)*(curDist-a.Dist)/(b.Dist-a.Dist)
	return ref - curElapsed, true
}

// RelativeRow is one line of the relative panel.
type RelativeRow struct {
	Place  int
	Driver string
	Gap    float64 // seconds to the player: + ahead of player, - behind
	Player bool
}

// RelativeRows returns up to `each` cars ahead and behind the player,
// ordered by place, with gaps relative to the player.
func RelativeRows(field []CarInfo, each int) []RelativeRow {
	var player *CarInfo
	for i := range field {
		if field[i].IsPlayer {
			player = &field[i]
			break
		}
	}
	if player == nil {
		return nil
	}
	sorted := make([]CarInfo, len(field))
	copy(sorted, field)
	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j].Place < sorted[i].Place {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}
	pi := -1
	for i := range sorted {
		if sorted[i].IsPlayer {
			pi = i
		}
	}
	if pi < 0 {
		return nil
	}
	lo := pi - each
	if lo < 0 {
		lo = 0
	}
	hi := pi + each
	if hi > len(sorted)-1 {
		hi = len(sorted) - 1
	}
	var rows []RelativeRow
	for i := lo; i <= hi; i++ {
		c := sorted[i]
		rows = append(rows, RelativeRow{
			Place:  c.Place,
			Driver: c.Driver,
			Gap:    player.BehindLeader - c.BehindLeader,
			Player: c.IsPlayer,
		})
	}
	return rows
}

// ---- session history: trace ribbons, delta reference, fuel per lap ----

var hist struct {
	mu           sync.Mutex
	recent       []Sample
	lastPushET   float64
	lap          int
	lapStartET   float64
	lapStartFuel float64
	curTrace     []TracePoint
	bestTrace    []TracePoint
	bestLap      float64
	lastLap      float64
	fuelPerLap   float64
}

func histPush(s *Sample) {
	hist.mu.Lock()
	defer hist.mu.Unlock()
	if hist.lastPushET != 0 {
		diff := s.ElapsedTime - hist.lastPushET
		if diff < -1 {
			// The session clock went backwards (session restarted): the old
			// tracking state is meaningless. Reset instead of throttling
			// forever against a stale clock — that froze the delta panel.
			hist.lap, hist.bestLap, hist.bestTrace = 0, 0, nil
			hist.curTrace = hist.curTrace[:0]
			hist.recent = hist.recent[:0]
			hist.fuelPerLap = 0
		} else if diff < 0.019 {
			return
		}
	}
	hist.lastPushET = s.ElapsedTime
	cp := *s
	cp.Field = nil
	hist.recent = append(hist.recent, cp)
	if len(hist.recent) > 1200 {
		hist.recent = hist.recent[len(hist.recent)-1200:]
	}
	if s.LapNumber <= 0 {
		return
	}
	if hist.lap == 0 {
		hist.lap, hist.lapStartET, hist.lapStartFuel = s.LapNumber, s.ElapsedTime, s.Fuel
		return
	}
	if s.LapNumber < hist.lap {
		// Lap counter went backwards (back to the garage / new run on the
		// same clock): track fresh from this lap.
		hist.lap, hist.lapStartET, hist.lapStartFuel = s.LapNumber, s.ElapsedTime, s.Fuel
		hist.curTrace = hist.curTrace[:0]
		return
	}
	if s.LapNumber > hist.lap {
		lt := s.ElapsedTime - hist.lapStartET
		if lt > 30 {
			hist.lastLap = lt
		}
		if lt > 30 && len(hist.curTrace) > 100 && (hist.bestLap == 0 || lt < hist.bestLap) {
			hist.bestLap = lt
			hist.bestTrace = append([]TracePoint(nil), hist.curTrace...)
		}
		if hist.lapStartFuel > s.Fuel+0.01 {
			hist.fuelPerLap = hist.lapStartFuel - s.Fuel
		}
		hist.lap, hist.lapStartET, hist.lapStartFuel = s.LapNumber, s.ElapsedTime, s.Fuel
		hist.curTrace = hist.curTrace[:0]
		return
	}
	if len(hist.curTrace) == 0 || s.LapDist > hist.curTrace[len(hist.curTrace)-1].Dist+2 {
		hist.curTrace = append(hist.curTrace, TracePoint{Dist: s.LapDist, Elapsed: s.ElapsedTime - hist.lapStartET})
	}
}

func histRecent(n int) []Sample {
	hist.mu.Lock()
	defer hist.mu.Unlock()
	if len(hist.recent) <= n {
		return append([]Sample(nil), hist.recent...)
	}
	return append([]Sample(nil), hist.recent[len(hist.recent)-n:]...)
}

func histDelta(s *Sample) (float64, bool) {
	hist.mu.Lock()
	defer hist.mu.Unlock()
	if hist.bestLap == 0 || s.LapNumber != hist.lap {
		return 0, false
	}
	return DeltaAgainstBest(hist.bestTrace, s.LapDist, s.ElapsedTime-hist.lapStartET)
}

func histLastLap() float64 {
	hist.mu.Lock()
	defer hist.mu.Unlock()
	return hist.lastLap
}

func histFuelPerLap() float64 {
	hist.mu.Lock()
	defer hist.mu.Unlock()
	return hist.fuelPerLap
}
