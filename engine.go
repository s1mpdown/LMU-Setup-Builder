// engine.go — the "Powered by Jett" generation logic.
// Given car + track + session + feel, it computes a complete setup plan.
// No numbers are typed by the user; every value is generated here.
// Lexus RC F values are calibrated from verified reference files.
// LLM-free, deterministic, unit-tested.
package main

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ---------- track knowledge ----------

type TrackInfo struct {
	Name  string
	Km    float64
	WingQ int
	WingR int
}

var trackTable = []TrackInfo{
	{"La Sarthe (Le Mans)", 13.63, 0, 0},
	{"Monza", 5.79, 0, 0},
	{"Daytona", 5.73, 0, 1},
	{"Sebring", 6.02, 1, 2},
	{"Silverstone", 5.89, 2, 2},
	{"Spa-Francorchamps", 7.00, 2, 2},
	{"Bahrain", 5.41, 2, 3},
	{"COTA", 5.51, 2, 3},
	{"Lusail", 5.42, 1, 2},
	{"Imola", 4.91, 2, 3},
	{"Portimao", 4.65, 3, 3},
	{"Fuji", 4.56, 2, 3},
	{"Interlagos", 4.31, 2, 3},
}

var defaultTrack = TrackInfo{"Unknown track", 5.70, 2, 2}

func lookupTrack(folder string) TrackInfo {
	f := strings.ToLower(folder)
	match := func(keys ...string) (TrackInfo, bool) {
		for _, t := range trackTable {
			name := strings.ToLower(t.Name)
			for _, k := range keys {
				if strings.Contains(f, k) || strings.Contains(name, f) && len(f) > 3 {
					return t, true
				}
			}
		}
		return defaultTrack, false
	}
	switch {
	case strings.Contains(f, "mans") || strings.Contains(f, "sarthe"):
		return trackTable[0]
	case strings.Contains(f, "monza"):
		return trackTable[1]
	case strings.Contains(f, "daytona"):
		return trackTable[2]
	case strings.Contains(f, "sebring"):
		return trackTable[3]
	case strings.Contains(f, "silverstone"):
		return trackTable[4]
	case strings.Contains(f, "spa"):
		return trackTable[5]
	case strings.Contains(f, "bahrain"):
		return trackTable[6]
	case strings.Contains(f, "cota") || strings.Contains(f, "austin"):
		return trackTable[7]
	case strings.Contains(f, "lusail") || strings.Contains(f, "qatar"):
		return trackTable[8]
	case strings.Contains(f, "imola"):
		return trackTable[9]
	case strings.Contains(f, "portim"):
		return trackTable[10]
	case strings.Contains(f, "fuji"):
		return trackTable[11]
	case strings.Contains(f, "interlagos") || strings.Contains(f, "paulo"):
		return trackTable[12]
	}
	if t, ok := match(); ok {
		return t
	}
	return defaultTrack
}

// ---------- physics-ish constants (derived from reference files) ----------

const (
	fuelPerKm   = 0.62 // litres per km, LMGT3 (Daytona measured 0.628 from Ari's own telemetry, 2026-10-31; Monza 0.587, Le Mans 0.566)
	vePerKm     = 0.70 // % of Virtual Energy per km at 100% pace
	secPerKm    = 18.6 // planning lap time, seconds per km
	tankLitres  = 100.0
	qualiLapsSm = 7 // out + 5 timed + in, tracks under 10 km
	qualiLapsLg = 4 // out + 2 timed + in, long tracks
)

// ---------- calibrated Lexus conversions ----------

// Bias: index 31 = 49.3 front %, index 32 = 49.0 -> 0.3 per click.
func biasToIndex(front float64) int { return 31 + int(math.Round((49.3-front)/0.3)) }

// Fuel: index = round(ratio*100) - 1.
func fuelToIndex(ratio float64) int { return int(math.Round(ratio*100)) - 1 }

// ---------- per-car profiles ----------

// CarProfile carries everything that differs between cars: the bias
// index scale, the quali/race bias points, electronics, preload,
// radiator habit and the wing index that means "low drag" on that car.
type CarProfile struct {
	Name         string
	Short        string  // used in file names
	BiasIdxAt    int     // an index whose value is known
	BiasValAt    float64 // front % at that index
	BiasStep     float64 // front % per click (value falls as index rises)
	QualiBias    float64
	RaceBias     float64
	TCMap, TCCut int
	SlipQ, SlipR int
	ABS          int
	PreloadQ     int
	PreloadR     int
	PreloadUnit  string
	RadQ, RadR   int
	WingBase     int // wing index equal to wing-table 0 (low drag)
}

func (c CarProfile) BiasIndex(front float64) int {
	return c.BiasIdxAt + int(math.Round((c.BiasValAt-front)/c.BiasStep))
}

var lexusProfile = CarProfile{
	Name: "Lexus RC F LMGT3", Short: "RCF",
	BiasIdxAt: 31, BiasValAt: 49.3, BiasStep: 0.3,
	QualiBias: 49.0, RaceBias: 49.3,
	TCMap: 6, TCCut: 6, SlipQ: 5, SlipR: 6, ABS: 9,
	PreloadQ: 80, PreloadR: 90, PreloadUnit: " Nm",
	RadQ: 10, RadR: 0, WingBase: 0,
}

// Hub-reference profiles: base templates and calibration pulled from
// Ultimate Setup Hub community setups (public API, 2026-10-31). Bias scales
// were derived from pairs of real files per car (all 0.25/click except the
// Lexus at 0.3). These are community starting points, not pro-team data.
var astonProfile = CarProfile{
	Name: "Aston Martin Vantage GT3", Short: "AMR",
	BiasIdxAt: 29, BiasValAt: 49.8, BiasStep: 0.25,
	QualiBias: 49.3, RaceBias: 49.8,
	TCMap: 4, TCCut: 4, SlipQ: 8, SlipR: 8, ABS: 9,
	PreloadQ: 24, PreloadR: 24, PreloadUnit: "",
	RadQ: 0, RadR: 0, WingBase: 0,
}

var bmwProfile = CarProfile{
	Name: "BMW M4 GT3", Short: "M4",
	BiasIdxAt: 32, BiasValAt: 49.0, BiasStep: 0.25,
	QualiBias: 48.5, RaceBias: 49.0,
	TCMap: 5, TCCut: 6, SlipQ: 5, SlipR: 5, ABS: 9,
	PreloadQ: 18, PreloadR: 18, PreloadUnit: "",
	RadQ: 0, RadR: 0, WingBase: 0,
}

var corvetteProfile = CarProfile{
	Name: "Corvette Z06 GT3.R", Short: "Z06",
	BiasIdxAt: 22, BiasValAt: 51.5, BiasStep: 0.25,
	QualiBias: 51.0, RaceBias: 51.5,
	TCMap: 8, TCCut: 4, SlipQ: 10, SlipR: 10, ABS: 9,
	PreloadQ: 24, PreloadR: 24, PreloadUnit: "",
	RadQ: 0, RadR: 0, WingBase: 0,
}

var ferrariProfile = CarProfile{
	Name: "Ferrari 296 GT3", Short: "F296",
	BiasIdxAt: 24, BiasValAt: 51.0, BiasStep: 0.25,
	QualiBias: 50.5, RaceBias: 51.0,
	TCMap: 5, TCCut: 5, SlipQ: 7, SlipR: 7, ABS: 9,
	PreloadQ: 50, PreloadR: 50, PreloadUnit: " Nm",
	RadQ: 0, RadR: 0, WingBase: 1,
}

var fordProfile = CarProfile{
	Name: "Ford Mustang GT3", Short: "MST",
	BiasIdxAt: 32, BiasValAt: 49.0, BiasStep: 0.25,
	QualiBias: 48.5, RaceBias: 49.0,
	TCMap: 6, TCCut: 6, SlipQ: 11, SlipR: 11, ABS: 9,
	PreloadQ: 65, PreloadR: 65, PreloadUnit: " Nm",
	RadQ: 0, RadR: 0, WingBase: 2,
}

var lamboProfile = CarProfile{
	Name: "Lamborghini Huracán GT3", Short: "HUR",
	BiasIdxAt: 30, BiasValAt: 49.5, BiasStep: 0.25,
	QualiBias: 49.0, RaceBias: 49.5,
	TCMap: 3, TCCut: 3, SlipQ: 1, SlipR: 1, ABS: 9,
	PreloadQ: 110, PreloadR: 110, PreloadUnit: "",
	RadQ: 0, RadR: 0, WingBase: 0,
}

var amgProfile = CarProfile{
	Name: "Mercedes-AMG GT3", Short: "AMG",
	BiasIdxAt: 24, BiasValAt: 51.0, BiasStep: 0.25,
	QualiBias: 48.5, RaceBias: 49.0,
	TCMap: 4, TCCut: 4, SlipQ: 3, SlipR: 3, ABS: 9,
	PreloadQ: 30, PreloadR: 30, PreloadUnit: "",
	RadQ: 0, RadR: 0, WingBase: 0,
}

var porscheProfile = CarProfile{
	Name: "Porsche 992 GT3 R", Short: "P992",
	BiasIdxAt: 42, BiasValAt: 46.5, BiasStep: 0.25,
	QualiBias: 46.0, RaceBias: 46.5,
	TCMap: 6, TCCut: 6, SlipQ: 9, SlipR: 9, ABS: 9,
	PreloadQ: 115, PreloadR: 115, PreloadUnit: "",
	RadQ: 0, RadR: 0, WingBase: 0,
}

var mclarenProfile = CarProfile{
	Name: "McLaren 720S LMGT3", Short: "720S",
	BiasIdxAt: 40, BiasValAt: 47.0, BiasStep: 0.25,
	QualiBias: 47.0, RaceBias: 48.0,
	TCMap: 5, TCCut: 5, SlipQ: 2, SlipR: 6, ABS: 9,
	PreloadQ: 15, PreloadR: 25, PreloadUnit: "",
	RadQ: 0, RadR: 0, WingBase: 10,
}

// carProfiles is the built-in list, parallel to carTemplates in main.go.
// Indexes 0-1 are Jett-calibrated (pro sources); 2+ are hub references.
var carProfiles = []CarProfile{
	lexusProfile, mclarenProfile,
	astonProfile, bmwProfile, corvetteProfile, ferrariProfile,
	fordProfile, lamboProfile, amgProfile, porscheProfile,
}

func clampF(v, lo, hi float64) float64 { return math.Min(hi, math.Max(lo, v)) }

// ---------- the plan ----------

type Session struct {
	Kind    string // "quali", "race20", "race30", "race60"
	Minutes int
	VECap   int
}

type Plan struct {
	BiasFront float64
	FuelRatio float64
	VE        int
	Wing      int
	TCMap     int
	TCCut     int
	TCSlip    int
	ABS       int
	Preload   int
	Radiators int // 0 open, 10 closed
	Stops     int
	Summary   []string
}

func carPlan(c CarProfile, t TrackInfo, s Session, feel string) Plan {
	var p Plan
	lapTime := t.Km * secPerKm
	feelBias, feelSlip := 0.0, 0
	switch feel {
	case "Stable":
		feelBias, feelSlip = 0.6, 1
	case "Rotating":
		feelBias, feelSlip = -0.6, -1
	}
	if s.Kind == "quali" {
		laps := qualiLapsSm
		if t.Km > 10 {
			laps = qualiLapsLg
		}
		p.BiasFront = c.QualiBias + feelBias
		p.FuelRatio = clampF(float64(laps)*t.Km*fuelPerKm/tankLitres, 0.10, 0.60)
		p.VE = 100
		p.Wing = c.WingBase + t.WingQ
		p.TCMap, p.TCCut, p.TCSlip = c.TCMap, c.TCCut, maxI(1, c.SlipQ+feelSlip)
		p.ABS = c.ABS
		p.Preload = c.PreloadQ
		p.Radiators = c.RadQ
		p.Summary = []string{
			fmt.Sprintf("Brake bias %.1f front — the %s quali point.", p.BiasFront, c.Name),
			fmt.Sprintf("Fuel %.2f (~%d L) — %d quali laps at %s, with margin.", p.FuelRatio, int(p.FuelRatio*tankLitres), laps, t.Name),
			"Medium tyres, radiators closed for drag — it's a quali lap, not a stint.",
			fmt.Sprintf("TC %d/%d, ABS %d — the car's proven electronics window.", p.TCMap, p.TCCut, p.ABS),
		}
		return p
	}
	// race
	raceLaps := int(math.Ceil(float64(s.Minutes)*60/lapTime)) + 1
	stintLaps := raceLaps
	if s.VECap < 100 {
		stintLaps = int(float64(s.VECap) / (vePerKm * t.Km))
		if stintLaps < 1 {
			stintLaps = 1
		}
	}
	used := stintLaps
	if raceLaps < used {
		used = raceLaps
	}
	p.Stops = int(math.Ceil(float64(raceLaps)/float64(used))) - 1
	p.BiasFront = c.RaceBias + feelBias
	p.FuelRatio = clampF(float64(used)*t.Km*fuelPerKm/tankLitres*1.03, 0.15, 0.97)
	p.VE = s.VECap
	p.Wing = c.WingBase + t.WingR
	p.TCMap, p.TCCut, p.TCSlip = c.TCMap, c.TCCut, maxI(1, c.SlipR+feelSlip)
	p.ABS = c.ABS
	p.Preload = c.PreloadR
	p.Radiators = c.RadR
	p.Summary = []string{
		fmt.Sprintf("Brake bias %.1f front — race spec, one step up from quali for stint stability.", p.BiasFront),
		fmt.Sprintf("Race is ~%d laps; stint plan %d laps on %d%% VE -> %d stop(s).", raceLaps, used, s.VECap, p.Stops),
		fmt.Sprintf("Fuel %.2f (~%d L) fills stint 1 with a splash of margin.", p.FuelRatio, int(p.FuelRatio*tankLitres)),
		map[bool]string{true: "Radiators open — an hour of heat beats a whisper of drag.", false: "Radiators stay as this car likes them."}[p.Radiators == 0],
		fmt.Sprintf("Preload up to %d%s for traction over the stint; TC slip %d lets it move.", p.Preload, c.PreloadUnit, p.TCSlip),
	}
	if feel != "Balanced" {
		p.Summary = append(p.Summary, fmt.Sprintf("Feel: %s — bias and TC slip nudged your way.", feel))
	}
	return p
}

func lexusPlan(t TrackInfo, s Session, feel string) Plan {
	return carPlan(lexusProfile, t, s, feel)
}

func maxI(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// ---------- .svm file handling ----------

func setSetting(lines []string, key string, idx int, comment string) []string {
	prefix := key + "Setting="
	for i, ln := range lines {
		if strings.HasPrefix(ln, prefix) {
			lines[i] = fmt.Sprintf("%s%d//%s", prefix, idx, comment)
			return lines
		}
	}
	return lines
}

func getSetting(lines []string, key string) (int, string, bool) {
	prefix := key + "Setting="
	re := regexp.MustCompile(`^` + regexp.QuoteMeta(key) + `Setting=(\d+)//(.*)$`)
	for _, ln := range lines {
		if strings.HasPrefix(ln, prefix) {
			if m := re.FindStringSubmatch(ln); m != nil {
				v, _ := strconv.Atoi(m[1])
				return v, m[2], true
			}
		}
	}
	return 0, "", false
}

func setNotes(lines []string, note string) []string {
	for i, ln := range lines {
		if strings.HasPrefix(ln, "Notes=") {
			lines[i] = "Notes=\"" + note + "\""
			return lines
		}
	}
	return lines
}

func toLines(content string) []string {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	return strings.Split(strings.TrimRight(content, "\n"), "\n")
}

func writeSVM(path string, lines []string) error {
	if _, err := os.Stat(path); err == nil {
		trackDir := filepath.Dir(path)
		playerDir := filepath.Dir(filepath.Dir(trackDir))
		bakDir := filepath.Join(playerDir, "JettBackups", filepath.Base(trackDir))
		if err := os.MkdirAll(bakDir, 0755); err != nil {
			return err
		}
		bak := filepath.Join(bakDir, filepath.Base(path)+".bak-"+time.Now().Format("20060102-150405"))
		if err := os.Rename(path, bak); err != nil {
			return err
		}
	}
	return os.WriteFile(path, []byte(strings.Join(lines, "\r\n")+"\r\n"), 0644)
}

// basePerLapLitres parses a template's own fuel comment:
// "FuelCapacitySetting=0//18.0L (5.3 laps)" -> 3.396 L/lap.
func basePerLapLitres(lines []string) (float64, bool) {
	re := regexp.MustCompile(`([\d.]+)L \(([\d.]+) laps\)`)
	for _, ln := range lines {
		if strings.HasPrefix(ln, "FuelCapacitySetting=") {
			if m := re.FindStringSubmatch(ln); m != nil {
				lit, _ := strconv.ParseFloat(m[1], 64)
				laps, _ := strconv.ParseFloat(m[2], 64)
				if laps > 0 {
					return lit / laps, true
				}
			}
		}
	}
	return 0, false
}

// applyPlan writes a full plan into template lines.
func applyPlan(lines []string, c CarProfile, p Plan, note string) []string {
	lines = setNotes(lines, note)
	lines = setSetting(lines, "RearBrake", c.BiasIndex(p.BiasFront),
		fmt.Sprintf("%.1f:%.1f", p.BiasFront, 100-p.BiasFront))
	lines = setSetting(lines, "Fuel", fuelToIndex(p.FuelRatio),
		strconv.FormatFloat(p.FuelRatio, 'f', 2, 64))
	lines = setSetting(lines, "VirtualEnergy", p.VE, fmt.Sprintf("%d%%", p.VE))
	lines = setSetting(lines, "RW", p.Wing, "deg (index)")
	lines = setSetting(lines, "TractionControlMap", p.TCMap, strconv.Itoa(p.TCMap))
	lines = setSetting(lines, "TCPowerCutMap", p.TCCut, strconv.Itoa(p.TCCut))
	lines = setSetting(lines, "TCSlipAngleMap", p.TCSlip, strconv.Itoa(p.TCSlip))
	lines = setSetting(lines, "AntilockBrakeSystemMap", p.ABS, strconv.Itoa(p.ABS))
	lines = setSetting(lines, "DiffPreload", p.Preload, fmt.Sprintf("%d%s", p.Preload, c.PreloadUnit))
	radLabel := "Open"
	if p.Radiators == 10 {
		radLabel = "Closed"
	}
	lines = setSetting(lines, "WaterRadiator", p.Radiators, radLabel)
	lines = setSetting(lines, "OilRadiator", p.Radiators, radLabel)
	return lines
}

// ---------- game location ----------

func settingsDir(lmu string) string {
	return filepath.Join(lmu, "UserData", "player", "Settings")
}

func looksLikeLMU(p string) bool {
	if p == "" {
		return false
	}
	st, err := os.Stat(filepath.Join(p, "UserData", "player"))
	return err == nil && st.IsDir()
}

func detectLMU() string {
	candidates := []string{
		`D:\SteamLibrary\steamapps\common\Le Mans Ultimate`,
		`C:\SteamLibrary\steamapps\common\Le Mans Ultimate`,
		`C:\Program Files (x86)\Steam\steamapps\common\Le Mans Ultimate`,
		`D:\Steam\steamapps\common\Le Mans Ultimate`,
		`E:\SteamLibrary\steamapps\common\Le Mans Ultimate`,
	}
	vdf := `C:\Program Files (x86)\Steam\steamapps\libraryfolders.vdf`
	if b, err := os.ReadFile(vdf); err == nil {
		re := regexp.MustCompile(`"path"\s*"([^"]+)"`)
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			lib := strings.ReplaceAll(m[1], "\\\\", "\\")
			candidates = append(candidates, filepath.Join(lib, "steamapps", "common", "Le Mans Ultimate"))
		}
	}
	for _, c := range candidates {
		if looksLikeLMU(c) {
			return c
		}
	}
	return ""
}

func listDirs(path string) []string {
	var out []string
	ents, err := os.ReadDir(path)
	if err == nil {
		for _, e := range ents {
			if e.IsDir() {
				out = append(out, e.Name())
			}
		}
	}
	sort.Strings(out)
	return out
}

func listSVMsRecursive(root string) []string {
	var out []string
	dirs := listDirs(root)
	for _, d := range dirs {
		ents, err := os.ReadDir(filepath.Join(root, d))
		if err != nil {
			continue
		}
		for _, e := range ents {
			if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".svm") {
				out = append(out, d+"\\"+e.Name())
			}
		}
	}
	sort.Strings(out)
	return out
}

func sanitize(s string) string {
	return regexp.MustCompile(`[^A-Za-z0-9]+`).ReplaceAllString(s, "")
}
