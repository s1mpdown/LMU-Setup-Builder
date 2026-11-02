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
	"strings"
	"sync"
)

// Each mapped file starts with an 8-byte version block (two counters the
// writer bumps before/after each update), then the payload struct.
const smVersionBlockSize = 8

// Telemetry payload offsets.
const (
	tNumVehicles = 4
	tVehicles    = 8
	tVehStride   = 1888

	vID            = 0
	vElapsedTime   = 12
	vLapNumber     = 20
	vVehicleName   = 32
	vTrackName     = 96
	vPos           = 160 // rF2Vec3: world position x, y, z doubles
	vLocalVel      = 184 // rF2Vec3: x, y, z doubles
	vGear          = 352
	vEngineRPM     = 356
	vUnfThrottle   = 388
	vUnfBrake      = 396
	vUnfSteering   = 404
	vFilThrottle   = 420
	vFilBrake      = 428
	vFuel          = 524
	vMaxRPM        = 532
	vDetached      = 542
	vDentSeverity  = 544
	vSector        = 600
	vSpeedLimiter  = 604 // byte: pit limiter engaged
	vFuelCapacity  = 608
	vLimiterAvail  = 656 // byte: limiter available in this car/session
	vRearBrakeBias = 664 // double: fraction of braking on the rear axle
	vWheels        = 848

	wheelStride  = 260
	wPressure    = 120
	wTemperature = 128 // three doubles across the tread; centre is +8
	wFlat        = 177
	wDetachedW   = 178
	wWear        = 152
	wCarcassTemp = 204 // mTireCarcassTemperature — the stable core temp the game shows
	wInnerTemp   = 212 // mTireInnerLayerTemperature
)

// Scoring payload offsets.
const (
	sInfo         = 4
	siTrackName   = 0
	siSession     = 64
	siCurrentET   = 68
	siEndET       = 76
	siMaxLaps     = 84
	siLapDist     = 88 // track length in metres
	siNumVeh      = 104
	siGamePhase   = 108
	siYellowFlag  = 109 // signed char: full-course yellow state
	siSectorFlag  = 110 // signed char[3]: yellow per sector
	siRaining     = 220
	siAmbientTemp = 228
	siTrackTemp   = 236

	sVehicles     = 552
	sVehStride    = 584
	svID          = 0
	svDriverName  = 4
	svVehName     = 36
	svTotalLaps   = 100
	svSector      = 102
	svLapDist     = 104
	svBestLap     = 144
	svLastSector1 = 152
	svLastSector2 = 160
	svLastLap     = 168
	svCurSector1  = 176
	svCurSector2  = 184
	svPitstops    = 192
	svIsPlayer    = 196
	svInPits      = 198
	svPlace       = 199
	svVehClass    = 200 // char[32]
	svBehindNext  = 232
	svBehindLead  = 244
	svLocalVel    = 288 // rF2Vec3: local velocity x, y, z doubles (layout dump, VehicleScoringInfoV01)
	svPitState    = 457
	svFlag        = 504 // 0 = green, 6 = blue (being shown to this car)
	svBestLapS1   = 528 // float: sector 1 split of the car's best lap
	svBestLapS2   = 532 // float: sector 2 split of the car's best lap
)

// Full mapped-file sizes (version block + payload).
const (
	telemetryFileSize = smVersionBlockSize + tVehicles + 128*tVehStride
	scoringFileSize   = smVersionBlockSize + sVehicles + 128*sVehStride
)

// Sample is one snapshot of the player's car.
type Sample struct {
	OK            bool
	ElapsedTime   float64
	LapNumber     int
	LapDist       float64
	TrackLength   float64
	TrackName     string
	VehicleName   string
	SpeedKmh      float64
	Throttle      float64 // unfiltered (the driver's actual foot), 0..1
	Brake         float64
	Steering      float64 // -1..1
	ThrottleF     float64 // filtered (what the car applied)
	BrakeF        float64
	Gear          int
	RPM           float64
	MaxRPM        float64 // engine's rev ceiling from the feed (0 = unknown)
	Fuel          float64
	FuelCapacity  float64 // litres, from the telemetry vehicle
	Sector        int
	InPits        bool
	Place         int
	Class         string // player's class (scoring mVehicleClass)
	LastLap       float64
	BestLap       float64
	CurSector1    float64 // current lap's sector splits (cumulative seconds)
	CurSector2    float64
	BestLapS1     float64 // best lap's sector splits (cumulative seconds)
	BestLapS2     float64
	SessionET     float64 // session clock now
	SessionLeft   float64 // seconds left in a timed session; -1 when unknown
	Session       int     // scoring header mSession (0=testday, 1-4=practice, 5-8=qual, 9=warmup, 10-13=race)
	MaxLaps       int
	Raining       float64 // 0..1 from the scoring header
	TrackTemp     float64
	AmbientTemp   float64
	GamePhase     int     // scoring header phase (5 = green running)
	YellowState   int     // full-course yellow state (<0/0 = none)
	SectorFlags   [3]int  // per-sector yellow flags (nonzero = yellow)
	PosX          float64 // world position (metres) — track learning + review
	PosY          float64
	PosZ          float64
	RearBrakeBias float64 // fraction of braking on the rear axle (0..1)
	SpeedLimiter  bool
	LimiterAvail  bool
	PitState      int // 0 none, 1 requested, 2 entering, 3 stopped, 4 exiting
	PitStops      int
	Dents         [8]int // dent severity 0..2 at 8 points around the car
	PartsOff      bool   // any bodywork detached
	TyreFlat      [4]bool
	TyreOff       [4]bool
	MenuCat       string      // pit MFD category currently shown ("" = no data)
	MenuChoice    string      // pit MFD choice currently selected
	TyreWear      [4]float64  // fraction remaining, FL FR RL RR
	TyrePress     [4]float64  // kPa
	TyreTemp      [4]float64  // Celsius, centre of tread (surface)
	TyreCarcass   [4]float64  // Celsius, carcass (core) — what the game displays
	TyreInner     [4]float64  // Celsius, inner layer
	Field         []CarInfo   // every car in scoring order
	FieldDents    map[int]int // dent severity sum per vehicle ID (telemetry buffer, all cars)
}

// CarInfo is one scoring entry — the raw material for the tower panel.
type CarInfo struct {
	ID           int
	Driver       string
	Vehicle      string
	Class        string
	Place        int // overall place
	TotalLaps    int
	Sector       int
	LapDist      float64
	BehindLeader float64 // seconds behind the leader
	BehindNext   float64 // seconds behind the car ahead (overall)
	LastLap      float64
	BestLap      float64
	PitStops     int
	IsPlayer     bool
	InPits       bool
	PitState     int
	Flag         int     // flag shown to this car (0 green, 6 blue)
	SpeedKmh     float64 // |mLocalVel| from scoring; used by the flag pill's hazard gate
}

func sf64(b []byte, off int) float64 {
	if off+8 > len(b) {
		return 0
	}
	return math.Float64frombits(binary.LittleEndian.Uint64(b[off:]))
}

func sf32(b []byte, off int) float64 {
	if off+4 > len(b) {
		return 0
	}
	return float64(math.Float32frombits(binary.LittleEndian.Uint32(b[off:])))
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
	s.Session = si32(info, siSession)
	s.SessionLeft = -1
	if endET := sf64(info, siEndET); endET > s.SessionET {
		s.SessionLeft = endET - s.SessionET
	}
	s.MaxLaps = si32(info, siMaxLaps)
	s.Raining = sf64(info, siRaining)
	s.TrackTemp = sf64(info, siTrackTemp)
	s.AmbientTemp = sf64(info, siAmbientTemp)
	s.GamePhase = int(info[siGamePhase])
	s.YellowState = int(int8(info[siYellowFlag]))
	for i := 0; i < 3; i++ {
		s.SectorFlags[i] = int(int8(info[siSectorFlag+i]))
	}

	// The whole field, for the tower panel.
	for i := 0; i < nSco; i++ {
		vb := sco[sVehicles+i*sVehStride:]
		if len(vb) < sVehStride {
			break
		}
		s.Field = append(s.Field, CarInfo{
			ID:           si32(vb, svID),
			Driver:       sstr(vb, svDriverName, 32),
			Vehicle:      sstr(vb, svVehName, 64),
			Class:        sstr(vb, svVehClass, 32),
			Place:        int(vb[svPlace]),
			TotalLaps:    int(int16(binary.LittleEndian.Uint16(vb[svTotalLaps:]))),
			Sector:       int(int8(vb[svSector])),
			LapDist:      sf64(vb, svLapDist),
			BehindLeader: sf64(vb, svBehindLead),
			BehindNext:   sf64(vb, svBehindNext),
			LastLap:      sf64(vb, svLastLap),
			BestLap:      sf64(vb, svBestLap),
			PitStops:     int(int16(binary.LittleEndian.Uint16(vb[svPitstops:]))),
			IsPlayer:     vb[svIsPlayer] != 0,
			InPits:       vb[svInPits] != 0,
			PitState:     int(vb[svPitState]),
			Flag:         int(vb[svFlag]),
			SpeedKmh:     math.Sqrt(math.Pow(sf64(vb, svLocalVel), 2)+math.Pow(sf64(vb, svLocalVel+8), 2)+math.Pow(sf64(vb, svLocalVel+16), 2)) * 3.6,
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
			s.CurSector1 = sf64(vb, svCurSector1)
			s.CurSector2 = sf64(vb, svCurSector2)
			s.BestLapS1 = sf32(vb, svBestLapS1)
			s.BestLapS2 = sf32(vb, svBestLapS2)
			s.Place = int(vb[svPlace])
			s.Class = sstr(vb, svVehClass, 32)
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
	s.MaxRPM = sf64(vb, vMaxRPM)
	s.Fuel = sf64(vb, vFuel)
	s.FuelCapacity = sf64(vb, vFuelCapacity)
	s.PosX = sf64(vb, vPos)
	s.PosY = sf64(vb, vPos+8)
	s.PosZ = sf64(vb, vPos+16)
	s.RearBrakeBias = sf64(vb, vRearBrakeBias)
	s.SpeedLimiter = vb[vSpeedLimiter] != 0
	s.LimiterAvail = vb[vLimiterAvail] != 0
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
		s.TyreCarcass[w] = sf64(wb, wCarcassTemp)
		s.TyreInner[w] = sf64(wb, wInnerTemp)
		s.TyreWear[w] = sf64(wb, wWear)
		s.TyreFlat[w] = wb[wFlat] != 0
		s.TyreOff[w] = wb[wDetachedW] != 0
	}

	// Dent sums for EVERY car in the telemetry buffer — the tower's DMG
	// token reads rivals from here. (Whether LMU actually populates
	// rivals' dents is a field question; dead data simply never shows.)
	if nTel > 0 {
		s.FieldDents = map[int]int{}
		for i := 0; i < nTel; i++ {
			vb2 := tel[tVehicles+i*tVehStride:]
			if len(vb2) < tVehStride {
				break
			}
			sum := 0
			for k := 0; k < 8; k++ {
				sum += int(vb2[vDentSeverity+k])
			}
			if sum > 0 {
				s.FieldDents[si32(vb2, vID)] = sum
			}
		}
	}
	return s
}

// The first ten columns are the original v1.0 format and never change;
// v1.3 appends world position and the three tyre-temperature layers per
// wheel (ts = surface centre, ti = inner layer, tc = carcass). Readers
// must accept both widths.
const csvHeader = "et_s,lap,lap_dist_m,speed_kmh,throttle,brake,steering,gear,rpm,fuel_l," +
	"pos_x,pos_y,pos_z," +
	"ts_fl,ts_fr,ts_rl,ts_rr,ti_fl,ti_fr,ti_rl,ti_rr,tc_fl,tc_fr,tc_rl,tc_rr\n"

func csvRow(s Sample) string {
	return fmt.Sprintf("%.3f,%d,%.1f,%.1f,%.4f,%.4f,%.4f,%d,%.0f,%.2f,"+
		"%.2f,%.2f,%.2f,"+
		"%.1f,%.1f,%.1f,%.1f,%.1f,%.1f,%.1f,%.1f,%.1f,%.1f,%.1f,%.1f\n",
		s.ElapsedTime, s.LapNumber, s.LapDist, s.SpeedKmh,
		s.Throttle, s.Brake, s.Steering, s.Gear, s.RPM, s.Fuel,
		s.PosX, s.PosY, s.PosZ,
		s.TyreTemp[0], s.TyreTemp[1], s.TyreTemp[2], s.TyreTemp[3],
		s.TyreInner[0], s.TyreInner[1], s.TyreInner[2], s.TyreInner[3],
		s.TyreCarcass[0], s.TyreCarcass[1], s.TyreCarcass[2], s.TyreCarcass[3])
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
	// Stale-head guard: for a few samples at a line crossing, the
	// lap counter has already incremented while the distance still
	// reads the old lap's end — elapsed ~0 paired with ~track
	// length. That pairing is impossible mid-lap, and without the
	// guard the panel flashes a whole-lap-sized delta for a frame
	// at every crossing.
	if curElapsed < 2 && curDist > 1000 {
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
	trackLen     float64
	// Sector deltas for the delta panel: S1/S2 latch when the splits
	// appear during the current lap; S3 snapshots at each line crossing.
	secS1, secS2, secS3       float64
	secS1OK, secS2OK, secS3OK bool
	// lapTrusted: the current lap was caught at its start (a line
	// crossing, or tracking began at the line / in the crossing-skew
	// zone). A lap first seen mid-way has a late-started timer and
	// must never be adopted as reference/last/fuel data.
	lapTrusted bool
}

// lapStartTrusted reports whether a lap that begins at this sample
// can be timed honestly: tracking caught it at the line (LapDist
// <= 300 m) or in the line-crossing skew zone (LapDist still reading
// the old lap's end, >= 90% of the lap — it wraps to ~0 within a
// sample or two, and any lap completing from there has an empty trace
// and is rejected by the span gate anyway). A lap first seen mid-way
// — joining a session on track, a rolling start picked up late — has
// a timer that started late, so its "lap time" reads short: a 104.3 s
// partial over 90% of Daytona would otherwise out-rank real 108 s
// laps and poison the delta reference in exactly the sessions where
// practice-style replays (garage starts) never reproduce it.
func lapStartTrusted(s *Sample) bool {
	if s.LapDist <= 300 {
		return true
	}
	return s.TrackLength > 0 && s.LapDist >= 0.9*s.TrackLength
}

// traceCoverage returns the fraction of the lap's 100 m bins that
// contain at least one trace point. A driven lap fills ~every bin; a
// teleport/reset lap (the driver used reset-to-track) skips whole
// sectors, so its endpoints span the lap while its coverage does not.
func traceCoverage(tr []TracePoint, trackLen float64) float64 {
	if trackLen <= 0 || len(tr) == 0 {
		return 0
	}
	bins := int(math.Ceil(trackLen / 100))
	hit := make([]bool, bins)
	for _, p := range tr {
		if p.Dist < 0 {
			continue
		}
		b := int(p.Dist / 100)
		if b >= bins {
			b = bins - 1
		}
		hit[b] = true
	}
	n := 0
	for _, h := range hit {
		if h {
			n++
		}
	}
	return float64(n) / float64(bins)
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
			hist.lapTrusted = false
			hist.secS1OK, hist.secS2OK, hist.secS3OK = false, false, false
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
		hist.lapTrusted = lapStartTrusted(s)
		return
	}
	if s.LapNumber < hist.lap {
		// Lap counter went backwards (back to the garage / new run on the
		// same clock): track fresh from this lap.
		hist.lap, hist.lapStartET, hist.lapStartFuel = s.LapNumber, s.ElapsedTime, s.Fuel
		hist.lapTrusted = lapStartTrusted(s)
		hist.curTrace = hist.curTrace[:0]
		hist.secS1OK, hist.secS2OK, hist.secS3OK = false, false, false
		return
	}
	if s.TrackLength > 1000 {
		hist.trackLen = s.TrackLength
	}
	if s.LapNumber > hist.lap {
		lt := s.ElapsedTime - hist.lapStartET
		// A completed lap only counts when it was a FULL lap, verified
		// three ways: it was TIMED from its start (lapTrusted), the
		// trace SPANS the lap (first point at the line, last point past
		// 95%), and the trace COVERS the lap (no teleport-sized holes —
		// reset-to-track laps skip whole sectors while their endpoints
		// still span the lap, and mid-lap joins can reach 95% while
		// missing the first half-kilometre). The old reach-only check
		// let exactly those laps through. Partials must not poison the
		// reference lap, the last-lap time (it drives the engineer's
		// laps-left maths), or the learned fuel-per-lap.
		fullLap := hist.trackLen <= 0 || (hist.lapTrusted && len(hist.curTrace) > 0 &&
			hist.curTrace[0].Dist <= 300 &&
			hist.curTrace[len(hist.curTrace)-1].Dist >= 0.95*hist.trackLen &&
			traceCoverage(hist.curTrace, hist.trackLen) >= 0.95)
		if lt > 30 && fullLap {
			hist.lastLap = lt
		}
		if lt > 30 && len(hist.curTrace) > 100 && fullLap && (hist.bestLap == 0 || lt < hist.bestLap) {
			hist.bestLap = lt
			hist.bestTrace = append([]TracePoint(nil), hist.curTrace...)
		}
		if fullLap && hist.lapStartFuel > s.Fuel+0.01 {
			hist.fuelPerLap = hist.lapStartFuel - s.Fuel
		}
		// Sector 3 delta of the lap just finished: total vs best total.
		if s.LastLap > 0 && s.BestLap > 0 {
			hist.secS3, hist.secS3OK = s.BestLap-s.LastLap, true
		}
		hist.secS1OK, hist.secS2OK = false, false
		hist.lap, hist.lapStartET, hist.lapStartFuel = s.LapNumber, s.ElapsedTime, s.Fuel
		hist.lapTrusted = true // the new lap begins at this crossing
		hist.curTrace = hist.curTrace[:0]
		return
	}
	// Same lap: latch the sector deltas as their splits appear. The
	// splits are cumulative, so each delta is "versus best at that line".
	if !hist.secS1OK && s.CurSector1 > 0 && s.BestLapS1 > 0 {
		hist.secS1, hist.secS1OK = s.BestLapS1-s.CurSector1, true
	}
	if !hist.secS2OK && s.CurSector2 > 0 && s.BestLapS2 > 0 {
		hist.secS2, hist.secS2OK = s.BestLapS2-s.CurSector2, true
	}
	// Trace points for the current lap. The crossing sample is a trap:
	// the telemetry lap counter increments AT the line while the scoring
	// LapDist still reads the old lap's end (~track length) for a few
	// samples. Seeding the trace with that stale point used to poison
	// the whole lap — the monotonic rule below then rejected every real
	// point, the finished lap had 1-2 points, failed the >100 gate, and
	// no reference ever armed (Ari's "delta only woke up late in a
	// stint": it armed only on crossings where the wrap won the race).
	// So: an empty trace accepts only a start-of-lap distance (< 300 m —
	// the wrap lands within metres of the line, sometimes negative in
	// the pit lane); the stale tail is ignored until the wrap arrives.
	if len(hist.curTrace) == 0 {
		if s.LapDist < 300 {
			hist.curTrace = append(hist.curTrace, TracePoint{Dist: s.LapDist, Elapsed: s.ElapsedTime - hist.lapStartET})
		}
	} else if s.LapDist > hist.curTrace[len(hist.curTrace)-1].Dist+2 {
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

func histBestLap() float64 {
	hist.mu.Lock()
	defer hist.mu.Unlock()
	return hist.bestLap
}

// histSectors returns the latched sector deltas (current lap's S1/S2 as
// they complete, last completed lap's S3) in the INTERNAL convention:
// positive = AHEAD of best. The delta panel converts with deltaDisplay
// before showing them, so what Ari reads matches LMU's own widget
// (losing = positive/red) — see overlay.go.
func histSectors() (s1, s2, s3 float64, ok1, ok2, ok3 bool) {
	hist.mu.Lock()
	defer hist.mu.Unlock()
	return hist.secS1, hist.secS2, hist.secS3, hist.secS1OK, hist.secS2OK, hist.secS3OK
}

// estTotalLaps estimates the race's total lap count for the delta
// panel's LAP #/# readout: exact in a lap-counted race (MaxLaps),
// projected from the session clock and last lap time in a timed race.
// Returns 0 when neither is knowable yet.
func estTotalLaps(s Sample) int {
	if s.MaxLaps >= s.LapNumber && s.MaxLaps <= 1000 {
		return s.MaxLaps
	}
	last := histLastLap()
	if s.SessionLeft > 0 && last > 20 {
		return s.LapNumber + int(math.Ceil(s.SessionLeft/last))
	}
	return 0
}

func histFuelPerLap() float64 {
	hist.mu.Lock()
	defer hist.mu.Unlock()
	return hist.fuelPerLap
}
