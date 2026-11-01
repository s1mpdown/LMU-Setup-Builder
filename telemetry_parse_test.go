package main

import (
	"encoding/binary"
	"math"
	"strings"
	"testing"
)

func putF64(b []byte, off int, v float64) {
	binary.LittleEndian.PutUint64(b[off:], math.Float64bits(v))
}
func putI32(b []byte, off int, v int32) {
	binary.LittleEndian.PutUint32(b[off:], uint32(v))
}
func putStr(b []byte, off int, s string) { copy(b[off:], s) }

func TestParseSamplePlayer(t *testing.T) {
	tel := make([]byte, tVehicles+128*tVehStride)
	sco := make([]byte, sVehicles+128*sVehStride)
	putI32(tel, tNumVehicles, 2)
	// Another car in slot 0, the player in slot 1 (ID 7).
	putI32(tel, tVehicles+0*tVehStride+vID, 3)
	pv := tel[tVehicles+1*tVehStride:]
	putI32(pv, vID, 7)
	putF64(pv, vElapsedTime, 321.5)
	putI32(pv, vLapNumber, 5)
	putStr(pv, vVehicleName, "Lexus RC F")
	putF64(pv, vLocalVel, 50.0) // 50 m/s forward = 180 km/h
	putI32(pv, vGear, 4)
	putF64(pv, vEngineRPM, 6500)
	putF64(pv, vUnfThrottle, 0.75)
	putF64(pv, vUnfBrake, 0.10)
	putF64(pv, vFuel, 42.5)
	putF64(pv, vWheels+0*wheelStride+wWear, 0.93)

	info := sco[sInfo:]
	putStr(info, siTrackName, "Daytona")
	putF64(info, siLapDist, 5729.0)
	putI32(info, siNumVeh, 2)
	ps := sco[sVehicles+1*sVehStride:]
	putI32(ps, svID, 7)
	ps[svIsPlayer] = 1
	ps[svPlace] = 3
	putF64(ps, svLapDist, 2500.0)
	putF64(ps, svLastLap, 107.9)
	putF64(ps, svBestLap, 107.3)

	s := parseSample(tel, sco)
	if !s.OK {
		t.Fatal("sample not OK")
	}
	if s.VehicleName != "Lexus RC F" || s.TrackName != "Daytona" {
		t.Errorf("names = %q / %q", s.VehicleName, s.TrackName)
	}
	if math.Abs(s.SpeedKmh-180) > 0.01 {
		t.Errorf("speed = %v, want 180", s.SpeedKmh)
	}
	if s.Throttle != 0.75 || s.Brake != 0.10 || s.Gear != 4 || s.LapNumber != 5 {
		t.Errorf("inputs = %v %v gear %d lap %d", s.Throttle, s.Brake, s.Gear, s.LapNumber)
	}
	if s.Place != 3 || s.LapDist != 2500 || s.BestLap != 107.3 {
		t.Errorf("scoring = place %d dist %v best %v", s.Place, s.LapDist, s.BestLap)
	}
	if math.Abs(s.TyreWear[0]-0.93) > 1e-9 {
		t.Errorf("wear FL = %v", s.TyreWear[0])
	}
	row := csvRow(s)
	if !strings.HasPrefix(row, "321.500,5,2500.0,180.0,0.7500,0.1000,") {
		t.Errorf("csv row = %q", row)
	}
}

func TestEnsurePluginEnabledInsert(t *testing.T) {
	in := "{\n  \"ChatTransceiver.dll\": {\n    \" Enabled\": 1\n  }\n}\n"
	out, changed := ensurePluginEnabled(in)
	if !changed || !strings.Contains(out, pluginDLLName) || !strings.Contains(out, `" Enabled": 1`) {
		t.Fatalf("insert failed: changed=%v\n%s", changed, out)
	}
	if !strings.Contains(out, "ChatTransceiver") {
		t.Fatal("existing entry lost")
	}
	// Second run must be a no-op.
	out2, changed2 := ensurePluginEnabled(out)
	if changed2 || out2 != out {
		t.Fatal("second run should not change anything")
	}
}

func TestEnsurePluginEnabledFlip(t *testing.T) {
	in := "{\n  \"" + pluginDLLName + "\": {\n    \" Enabled\": 0,\n    \"UnsubscribedBuffersMask\": 160\n  }\n}\n"
	out, changed := ensurePluginEnabled(in)
	if !changed || !strings.Contains(out, `" Enabled": 1`) {
		t.Fatalf("flip failed: changed=%v\n%s", changed, out)
	}
	if !strings.Contains(out, `"UnsubscribedBuffersMask": 160`) {
		t.Fatal("other settings in the block must be preserved")
	}
}

func TestEnsurePluginEnabledMissingKey(t *testing.T) {
	in := "{\n  \"" + pluginDLLName + "\": {\n    \"DebugOutputLevel\": 0\n  }\n}\n"
	out, changed := ensurePluginEnabled(in)
	if !changed || !strings.Contains(out, `" Enabled": 1`) {
		t.Fatalf("missing-key insert failed: changed=%v\n%s", changed, out)
	}
}

func TestDeltaAgainstBest(t *testing.T) {
	best := []TracePoint{{0, 0}, {1000, 20}, {2000, 41}, {3000, 63}}
	d, ok := DeltaAgainstBest(best, 1500, 29.0)
	if !ok || math.Abs(d-1.5) > 1e-9 { // ref at 1500 = 30.5; 30.5-29 = +1.5 ahead
		t.Errorf("delta = %v ok=%v, want +1.5", d, ok)
	}
	if _, ok := DeltaAgainstBest(best, 10, 0.2); ok {
		t.Error("too early in lap should be not-ok")
	}
}

func TestRelativeRows(t *testing.T) {
	field := []CarInfo{
		{Place: 1, Driver: "A", BehindLeader: 0},
		{Place: 2, Driver: "B", BehindLeader: 2.0},
		{Place: 3, Driver: "YOU", BehindLeader: 5.5, IsPlayer: true},
		{Place: 4, Driver: "C", BehindLeader: 6.1},
		{Place: 5, Driver: "D", BehindLeader: 9.0},
	}
	rows := RelativeRows(field, 1)
	if len(rows) != 3 || rows[0].Driver != "B" || rows[2].Driver != "C" {
		t.Fatalf("rows = %+v", rows)
	}
	if math.Abs(rows[0].Gap-3.5) > 1e-9 || math.Abs(rows[2].Gap-(-0.6)) > 1e-9 {
		t.Errorf("gaps = %v / %v, want +3.5 / -0.6", rows[0].Gap, rows[2].Gap)
	}
}
