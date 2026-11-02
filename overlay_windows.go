//go:build windows

package main

// The in-game overlay: one transparent, always-on-top window covering the
// primary display, painted with plain GDI. Panels draw only while the
// telemetry feed is live (or in MOVE mode), each panel is individually
// toggleable, and in MOVE mode panels can be dragged by the body and
// resized by the bottom-right corner (free per-panel scale, persisted).
// The surface rebuilds itself when the display resolution changes and
// rescues any panel that would land off-screen. Requires the game in
// borderless/windowed mode — nothing can draw over exclusive fullscreen.

import (
	"fmt"
	"math"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	gdi32    = syscall.NewLazyDLL("gdi32.dll")
	ovKernel = syscall.NewLazyDLL("kernel32.dll")

	pRegisterClassExW  = user32.NewProc("RegisterClassExW")
	pCreateWindowExW   = user32.NewProc("CreateWindowExW")
	pDefWindowProcW    = user32.NewProc("DefWindowProcW")
	pPeekMessageW      = user32.NewProc("PeekMessageW")
	pTranslateMessage  = user32.NewProc("TranslateMessage")
	pDispatchMessageW  = user32.NewProc("DispatchMessageW")
	pBeginPaint        = user32.NewProc("BeginPaint")
	pEndPaint          = user32.NewProc("EndPaint")
	pInvalidateRect    = user32.NewProc("InvalidateRect")
	pShowWindow        = user32.NewProc("ShowWindow")
	pGetSystemMetrics  = user32.NewProc("GetSystemMetrics")
	pSetWindowPos      = user32.NewProc("SetWindowPos")
	pSetLayeredWinAttr = user32.NewProc("SetLayeredWindowAttributes")
	pGetWindowLongPtrW = user32.NewProc("GetWindowLongPtrW")
	pSetWindowLongPtrW = user32.NewProc("SetWindowLongPtrW")
	pGetDC             = user32.NewProc("GetDC")
	pReleaseDC         = user32.NewProc("ReleaseDC")
	pFillRect          = user32.NewProc("FillRect")

	pCreateCompatibleDC     = gdi32.NewProc("CreateCompatibleDC")
	pCreateCompatibleBitmap = gdi32.NewProc("CreateCompatibleBitmap")
	pSelectObject           = gdi32.NewProc("SelectObject")
	pDeleteDC               = gdi32.NewProc("DeleteDC")
	pDeleteObject           = gdi32.NewProc("DeleteObject")
	pBitBlt                 = gdi32.NewProc("BitBlt")
	pCreateSolidBrush       = gdi32.NewProc("CreateSolidBrush")
	pCreatePen              = gdi32.NewProc("CreatePen")
	pRectangle              = gdi32.NewProc("Rectangle")
	pEllipse                = gdi32.NewProc("Ellipse")
	pMoveToEx               = gdi32.NewProc("MoveToEx")
	pLineTo                 = gdi32.NewProc("LineTo")
	pCreateFontW            = gdi32.NewProc("CreateFontW")
	pSetTextColor           = gdi32.NewProc("SetTextColor")
	pSetBkMode              = gdi32.NewProc("SetBkMode")
	pTextOutW               = gdi32.NewProc("TextOutW")
	pGetTextExtentPoint32W  = gdi32.NewProc("GetTextExtentPoint32W")
	pGetModuleHandleW       = ovKernel.NewProc("GetModuleHandleW")
)

const (
	wsPopup         = 0x80000000
	exLayered       = 0x00080000
	exTransparent   = 0x00000020
	exTopmost       = 0x00000008
	exNoActivate    = 0x08000000
	exToolWindow    = 0x00000080
	lwaColorkey     = 0x00000001
	swHide          = 0
	swShow          = 5
	swShowNoActive  = 4
	wmPaint         = 0x000F
	wmLButtonDown   = 0x0201
	wmLButtonDblClk = 0x0203
	wmMouseMove     = 0x0200
	wmLButtonUp     = 0x0202
	srccopy         = 0x00CC0020
	transparentBk   = 1
	psSolid         = 0
	csDblClks       = 0x0008
	swpNoActivate   = 0x0010
	swpNoZOrder     = 0x0004
)

var gwlExStyle = int32(-20) // GWL_EXSTYLE, signed on purpose

func cref(r, g, b uint32) uintptr { return uintptr(r | g<<8 | b<<16) }

var (
	colKey      = cref(255, 0, 255)
	colSurface  = cref(0x11, 0x1d, 0x33)
	colSurf2    = cref(0x17, 0x2a, 0x4f)
	colGlassHi  = cref(0x1d, 0x33, 0x59) // frosted-look top band of a panel
	colBorder   = cref(0x2f, 0x44, 0x66)
	colPaper    = cref(0xf7, 0xf7, 0xff)
	colMuted    = cref(0xa8, 0xbc, 0xd6)
	colOrange   = cref(0xff, 0x9f, 0x1c)
	colTeal     = cref(0x2e, 0xc4, 0xb6)
	colRed      = cref(0xd7, 0x26, 0x3d)
	colSoftRed  = cref(0xff, 0x7a, 0x7a)
	colGreen    = cref(0x1f, 0xa3, 0x96)
	colBrake    = cref(0xff, 0x33, 0x55)
	colAmber    = cref(0xd7, 0xa0, 0x21)
	colDarkInk  = cref(0x14, 0x18, 0x22)
	colBlue     = cref(0x24, 0x56, 0xc8)
	colPurple   = cref(0xB0, 0x6A, 0xFF) // timing purple: fastest lap (preview11)
	colLimiter  = cref(0x3a, 0xa0, 0xff)
	colPitAmber = cref(0xff, 0xc0, 0x40)
)

// saveOverlayLayout is set by main.go to persist dragged positions / scales.
var saveOverlayLayout func()

// renderer state — touched only on the overlay thread.
var ovR struct {
	hwnd     uintptr
	memDC    uintptr
	bitmap   uintptr
	oldBmp   uintptr
	scrW     int
	scrH     int
	shown    bool
	editLive bool // click-through currently removed for MOVE mode
	brushes  map[uintptr]uintptr
	fonts    map[int]uintptr
	rects    map[string]rectT
	design   map[string][2]int // designed (unscaled) size per panel key
	dragKey  string
	dragMode string // "" | "move" | "size"
	dragDX   int
	dragDY   int
	sizeW0   int     // rect width when the resize grab started
	sizeF0   float64 // scale when the resize grab started
	maxRPM   float64 // self-calibrated rev ceiling for the RPM bar
}

func ovBrush(color uintptr) uintptr {
	if ovR.brushes == nil {
		ovR.brushes = map[uintptr]uintptr{}
	}
	if b, ok := ovR.brushes[color]; ok {
		return b
	}
	b, _, _ := pCreateSolidBrush.Call(color)
	ovR.brushes[color] = b
	return b
}

func ovFont(px int, bold bool) uintptr { return ovFontFace(px, bold, false) }

func ovMonoFont(px int, bold bool) uintptr { return ovFontFace(px, bold, true) }

func ovFontFace(px int, bold bool, mono bool) uintptr {
	if ovR.fonts == nil {
		ovR.fonts = map[int]uintptr{}
	}
	key := px * 4
	if bold {
		key += 1
	}
	if mono {
		key += 2
	}
	if f, ok := ovR.fonts[key]; ok {
		return f
	}
	weight := uintptr(400)
	if bold {
		weight = 700
	}
	faceName := "Segoe UI"
	if mono {
		faceName = "Consolas"
	}
	face, _ := syscall.UTF16PtrFromString(faceName)
	f, _, _ := pCreateFontW.Call(uintptr(-px), 0, 0, 0, weight, 0, 0, 0, 0, 0, 0, 0, 0, uintptr(unsafe.Pointer(face)))
	ovR.fonts[key] = f
	return f
}

type rectT struct{ L, T, R, B int32 }

func ovFill(dc uintptr, x, y, w, h int, color uintptr) {
	if w <= 0 || h <= 0 {
		return
	}
	r := rectT{int32(x), int32(y), int32(x + w), int32(y + h)}
	pFillRect.Call(dc, uintptr(unsafe.Pointer(&r)), ovBrush(color))
}

func ovFrame(dc uintptr, x, y, w, h int, color uintptr) {
	ovFill(dc, x, y, w, 1, color)
	ovFill(dc, x, y+h-1, w, 1, color)
	ovFill(dc, x, y, 1, h, color)
	ovFill(dc, x+w-1, y, 1, h, color)
}

func ovText(dc uintptr, x, y int, s string, px int, bold bool, color uintptr) {
	ovTextF(dc, x, y, s, px, bold, color, false)
}

func ovTextMono(dc uintptr, x, y int, s string, px int, bold bool, color uintptr) {
	ovTextF(dc, x, y, s, px, bold, color, true)
}

func ovTextF(dc uintptr, x, y int, s string, px int, bold bool, color uintptr, mono bool) {
	old, _, _ := pSelectObject.Call(dc, ovFontFace(px, bold, mono))
	pSetTextColor.Call(dc, color)
	u16, _ := syscall.UTF16FromString(s)
	if len(u16) > 1 {
		pTextOutW.Call(dc, uintptr(x), uintptr(y), uintptr(unsafe.Pointer(&u16[0])), uintptr(len(u16)-1))
	}
	pSelectObject.Call(dc, old)
}

// ovTextWidth measures a string in the given overlay font (used to
// lay out the tower row in segments so one field can change colour).
func ovTextWidth(dc uintptr, s string, px int, bold, mono bool) int {
	old, _, _ := pSelectObject.Call(dc, ovFontFace(px, bold, mono))
	var sz struct{ Cx, Cy int32 }
	u16, _ := syscall.UTF16FromString(s)
	w := 0
	if len(u16) > 1 {
		pGetTextExtentPoint32W.Call(dc, uintptr(unsafe.Pointer(&u16[0])), uintptr(len(u16)-1), uintptr(unsafe.Pointer(&sz)))
		w = int(sz.Cx)
	}
	pSelectObject.Call(dc, old)
	return w
}

func ovTextCenter(dc uintptr, cx, y int, s string, px int, bold bool, color uintptr) {
	ovTextCenterF(dc, cx, y, s, px, bold, color, false)
}

func ovTextCenterF(dc uintptr, cx, y int, s string, px int, bold bool, color uintptr, mono bool) {
	old, _, _ := pSelectObject.Call(dc, ovFontFace(px, bold, mono))
	var sz struct{ Cx, Cy int32 }
	u16, _ := syscall.UTF16FromString(s)
	if len(u16) > 1 {
		pGetTextExtentPoint32W.Call(dc, uintptr(unsafe.Pointer(&u16[0])), uintptr(len(u16)-1), uintptr(unsafe.Pointer(&sz)))
		pSetTextColor.Call(dc, color)
		pTextOutW.Call(dc, uintptr(cx-int(sz.Cx)/2), uintptr(y), uintptr(unsafe.Pointer(&u16[0])), uintptr(len(u16)-1))
	}
	pSelectObject.Call(dc, old)
}

// ovTextOutlined draws text with a dark outline (offsets + centre pass).
func ovTextOutlined(dc uintptr, cx, y int, s string, px int, bold bool, fg uintptr) {
	for _, o := range [][2]int{{-1, 0}, {1, 0}, {0, -1}, {0, 1}} {
		ovTextCenter(dc, cx+o[0], y+o[1], s, px, bold, colDarkInk)
	}
	ovTextCenter(dc, cx, y, s, px, bold, fg)
}

// ovPanel paints the shared glassy panel shell: deep-navy body, a
// lighter frosted-look strip across the top, and a soft border.
// (True per-pixel translucency isn't available in this colorkey
// renderer; the approved v1.3 look is this frosted approximation.)
// Preview4: the bright 2px accent line that used to sit under the
// top strip is gone — Ari hated it on every panel ("its everywhere
// on the panel"). Panel identity rides on the title text colour.
func ovPanel(dc uintptr, x, y, w, h int) {
	ovFill(dc, x, y, w, h, colSurface)
	ovFill(dc, x, y, w, 10, colGlassHi)
	ovFrame(dc, x, y, w, h, colBorder)
}

// ovStopwatch draws a tiny stopwatch centred at (cx, cy): a filled
// disc of diameter d (scaled px), the winder crown on top, and two
// pale hands (≈12 and ≈4:30). Vector-drawn — no font glyphs — so it
// stays crisp at any panel scale. Marks the tower's fastest-lap row.
func ovStopwatch(dc uintptr, cx, cy, d int, color uintptr) {
	if d < 6 {
		d = 6
	}
	top := cy - d/2
	cw := d / 3
	if cw < 2 {
		cw = 2
	}
	ovFill(dc, cx-cw/2, top-cw, cw, cw, color) // crown (stem merges into the dial)
	pen, _, _ := pCreatePen.Call(psSolid, 1, color)
	oldPen, _, _ := pSelectObject.Call(dc, pen)
	oldBrush, _, _ := pSelectObject.Call(dc, ovBrush(color))
	pEllipse.Call(dc, uintptr(cx-d/2), uintptr(top), uintptr(cx-d/2+d), uintptr(top+d))
	hpen, _, _ := pCreatePen.Call(psSolid, 1, colPaper)
	pSelectObject.Call(dc, hpen)
	r := d / 2
	pMoveToEx.Call(dc, uintptr(cx), uintptr(cy), 0)
	pLineTo.Call(dc, uintptr(cx), uintptr(cy-r+2))
	pMoveToEx.Call(dc, uintptr(cx), uintptr(cy), 0)
	pLineTo.Call(dc, uintptr(cx+r-2), uintptr(cy+r-2))
	pSelectObject.Call(dc, oldBrush)
	pSelectObject.Call(dc, oldPen)
	pDeleteObject.Call(pen)
	pDeleteObject.Call(hpen)
}

func ovRibbon(dc uintptr, pts []float64, x, y, w, h int, color uintptr) {
	if len(pts) < 2 {
		return
	}
	pen, _, _ := pCreatePen.Call(psSolid, 2, color)
	old, _, _ := pSelectObject.Call(dc, pen)
	step := float64(w) / float64(len(pts)-1)
	px := func(i int) int { return x + int(float64(i)*step) }
	py := func(v float64) int {
		if v < 0 {
			v = 0
		}
		if v > 1 {
			v = 1
		}
		return y + int((1-v)*float64(h))
	}
	pMoveToEx.Call(dc, uintptr(px(0)), uintptr(py(pts[0])), 0)
	for i, v := range pts {
		pLineTo.Call(dc, uintptr(px(i)), uintptr(py(v)))
	}
	pSelectObject.Call(dc, old)
	pDeleteObject.Call(pen)
}

type paintStruct struct {
	DC          uintptr
	Erase       int32
	RcPaint     rectT
	Restore     int32
	IncUpdate   int32
	RgbReserved [32]byte
}

// ovPanelKeys in hit-test priority (last painted = topmost).
var ovPanelKeys = []string{"engineer", "delta", "inputs", "tyres", "relative"}

var ovPanelLabels = map[string]string{
	"relative": "TOWER", "delta": "DELTA", "inputs": "INPUTS",
	"tyres": "TYRES", "engineer": "ENGINEER",
}

func ovHitTest(x, y int) (string, bool) {
	for _, key := range ovPanelKeys {
		if r, ok := ovR.rects[key]; ok {
			if x >= int(r.L) && x < int(r.R) && y >= int(r.T) && y < int(r.B) {
				corner := x >= int(r.R)-30 && y >= int(r.B)-30
				return key, corner
			}
		}
	}
	return "", false
}

var ovWndProc = syscall.NewCallback(func(hwnd, msg, wParam, lParam uintptr) uintptr {
	switch msg {
	case wmPaint:
		if ovR.memDC != 0 {
			var ps paintStruct
			dc, _, _ := pBeginPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
			pBitBlt.Call(dc, 0, 0, uintptr(ovR.scrW), uintptr(ovR.scrH), ovR.memDC, 0, 0, srccopy)
			pEndPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
			return 0
		}
	case wmLButtonDown:
		if ovState.Edit.Load() {
			x := int(int16(lParam & 0xFFFF))
			y := int(int16((lParam >> 16) & 0xFFFF))
			if key, corner := ovHitTest(x, y); key != "" {
				if r, ok := ovR.rects[key]; ok {
					ovR.dragKey = key
					if corner {
						ovR.dragMode = "size"
						ovR.sizeW0 = int(r.R - r.L)
						ovR.sizeF0 = ovPanelScale(key)
					} else {
						ovR.dragMode = "move"
						ovR.dragDX = x - int(r.L)
						ovR.dragDY = y - int(r.T)
					}
				}
			}
			return 0
		}
	case wmLButtonDblClk:
		if ovState.Edit.Load() {
			x := int(int16(lParam & 0xFFFF))
			y := int(int16((lParam >> 16) & 0xFFFF))
			if key, corner := ovHitTest(x, y); key != "" && corner {
				ovSetPanelScale(key, 1)
				ovR.dragKey, ovR.dragMode = "", ""
				if saveOverlayLayout != nil {
					saveOverlayLayout()
				}
			}
			return 0
		}
	case wmMouseMove:
		if ovR.dragKey != "" {
			x := int(int16(lParam & 0xFFFF))
			y := int(int16((lParam >> 16) & 0xFFFF))
			if ovR.dragMode == "size" {
				if r, ok := ovR.rects[ovR.dragKey]; ok && ovR.sizeW0 > 0 {
					newW := x - int(r.L)
					if newW > 10 {
						ovSetPanelScale(ovR.dragKey, ovR.sizeF0*float64(newW)/float64(ovR.sizeW0))
					}
				}
			} else {
				ovSetPanelPos(ovR.dragKey, x-ovR.dragDX, y-ovR.dragDY)
			}
			return 0
		}
	case wmLButtonUp:
		if ovR.dragKey != "" {
			ovR.dragKey, ovR.dragMode = "", ""
			if saveOverlayLayout != nil {
				saveOverlayLayout()
			}
			return 0
		}
	}
	r, _, _ := pDefWindowProcW.Call(hwnd, msg, wParam, lParam)
	return r
})

type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   uintptr
	Icon       uintptr
	Cursor     uintptr
	Background uintptr
	MenuName   *uint16
	ClassName  *uint16
	IconSm     uintptr
}

type msgT struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	PtX     int32
	PtY     int32
}

// placeholderSample makes every panel drawable in MOVE mode before the
// game feed exists, so panels can be arranged in the menus.
func placeholderSample() Sample {
	return Sample{
		OK: true, GamePhase: 5,
		TrackName: "Daytona International Speedway Road Course",
		Session:   13, SessionLeft: 2532, // tower context block preview (preview6)
		SpeedKmh: 167, Gear: 3, LapNumber: 4, Throttle: 0.35, Brake: 0.8,
		Fuel: 38.2, FuelCapacity: 100, RPM: 6800, MaxRPM: 7800,
		RearBrakeBias: 0.51, TrackTemp: 31, AmbientTemp: 24,
		TyreWear:    [4]float64{0.95, 0.94, 0.96, 0.95},
		TyreTemp:    [4]float64{96, 98, 94, 95},
		TyreCarcass: [4]float64{82, 84, 80, 81},
		TyrePress:   [4]float64{204, 205, 201, 202},
		FieldDents:  map[int]int{4: 5},
		Field: []CarInfo{
			{ID: 1, Place: 1, Driver: "HY LEADER", Class: "Hypercar", BehindLeader: 0, LastLap: 98.4, BestLap: 97.9, TotalLaps: 12},
			{ID: 2, Place: 2, Driver: "HY SECOND", Class: "Hypercar", BehindLeader: 2.4, LastLap: 98.9, BestLap: 98.1, TotalLaps: 12},
			{ID: 3, Place: 9, Driver: "GT LEADER", Class: "LMGT3", BehindLeader: 61.0, LastLap: 107.3, BestLap: 106.8, TotalLaps: 11},
			{ID: 4, Place: 11, Driver: "AHEAD 1", Class: "LMGT3", BehindLeader: 101.6, LastLap: 107.9, BestLap: 107.2, TotalLaps: 11},
			{ID: 5, Place: 12, Driver: "AHEAD 2", Class: "LMGT3", BehindLeader: 104.2, LastLap: 108.4, BestLap: 107.6, TotalLaps: 11},
			{ID: 6, Place: 13, Driver: "YOU", Class: "LMGT3", BehindLeader: 105.1, LastLap: 107.1, BestLap: 106.9, TotalLaps: 11, IsPlayer: true},
			{ID: 7, Place: 14, Driver: "BEHIND 1", Class: "LMGT3", BehindLeader: 106.4, LastLap: 108.8, BestLap: 107.9, TotalLaps: 11, PitStops: 1},
			{ID: 8, Place: 15, Driver: "BEHIND 2", Class: "LMGT3", BehindLeader: 109.2, LastLap: 109.5, BestLap: 108.6, TotalLaps: 11},
		},
	}
}

// ovRebuildSurface (re)creates the off-screen bitmap at the given size
// and moves/resizes the overlay window to cover the primary display.
func ovRebuildSurface(w, h int) {
	if ovR.memDC == 0 {
		dc0, _, _ := pGetDC.Call(0)
		memDC, _, _ := pCreateCompatibleDC.Call(dc0)
		ovR.memDC = memDC
		pReleaseDC.Call(0, dc0)
		pSetBkMode.Call(memDC, transparentBk)
	}
	if ovR.bitmap != 0 {
		pSelectObject.Call(ovR.memDC, ovR.oldBmp)
		pDeleteObject.Call(ovR.bitmap)
		ovR.bitmap = 0
	}
	dc0, _, _ := pGetDC.Call(0)
	bmp, _, _ := pCreateCompatibleBitmap.Call(dc0, uintptr(w), uintptr(h))
	old, _, _ := pSelectObject.Call(ovR.memDC, bmp)
	ovR.bitmap, ovR.oldBmp = bmp, old
	pReleaseDC.Call(0, dc0)
	ovR.scrW, ovR.scrH = w, h
	if ovR.hwnd != 0 {
		pSetWindowPos.Call(ovR.hwnd, ^uintptr(0), 0, 0, uintptr(w), uintptr(h), swpNoActivate|swpNoZOrder)
	}
	ovClampLayout()
}

// ovClampLayout rescues every saved panel position so the panel sits
// fully inside the current display. Applied at startup and after any
// resolution change — a layout saved on a bigger screen must never
// open off-screen (the old "panels lost in the corner" failure).
func ovClampLayout() {
	if ovR.scrW == 0 || ovR.scrH == 0 {
		return
	}
	for key, p := range ovAllPositions() {
		w, h := 200, 60
		if r, ok := ovR.rects[key]; ok {
			w, h = int(r.R-r.L), int(r.B-r.T)
		} else if d, ok := ovR.design[key]; ok {
			f := ovPanelScale(key)
			w, h = int(float64(d[0])*f), int(float64(d[1])*f)
		}
		x, y := p[0], p[1]
		if x < 0 {
			x = 0
		}
		if y < 0 {
			y = 0
		}
		if x+w > ovR.scrW {
			x = ovR.scrW - w
		}
		if y+h > ovR.scrH {
			y = ovR.scrH - h
		}
		if x < 0 {
			x = 0
		}
		if y < 0 {
			y = 0
		}
		if x != p[0] || y != p[1] {
			ovSetPanelPos(key, x, y)
			if saveOverlayLayout != nil {
				saveOverlayLayout()
			}
		}
	}
}

func overlayLoop() {
	runtime.LockOSThread()
	defer func() {
		if r := recover(); r != nil {
			logLine(fmt.Sprintf("overlay crashed: %v", r))
		}
	}()

	sw, _, _ := pGetSystemMetrics.Call(0)
	sh, _, _ := pGetSystemMetrics.Call(1)
	if int(sw) == 0 {
		logLine("overlay: no screen metrics")
		return
	}
	ovR.rects = map[string]rectT{}
	ovR.design = map[string][2]int{}

	hInst, _, _ := pGetModuleHandleW.Call(0)
	className, _ := syscall.UTF16PtrFromString("JettOverlayWindow")
	wc := wndClassEx{Style: csDblClks, WndProc: ovWndProc, Instance: hInst, ClassName: className}
	wc.Size = uint32(unsafe.Sizeof(wc))
	pRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))

	hwnd, _, _ := pCreateWindowExW.Call(
		exLayered|exTransparent|exTopmost|exNoActivate|exToolWindow,
		uintptr(unsafe.Pointer(className)),
		0, wsPopup,
		0, 0, uintptr(int(sw)), uintptr(int(sh)),
		0, 0, hInst, 0)
	if hwnd == 0 {
		logLine("overlay: CreateWindowEx failed")
		return
	}
	ovR.hwnd = hwnd
	pSetLayeredWinAttr.Call(hwnd, colKey, 0, lwaColorkey)
	ovRebuildSurface(int(sw), int(sh))

	logLine("overlay ready")
	var m msgT
	for {
		for {
			r, _, _ := pPeekMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0, 1)
			if r == 0 {
				break
			}
			pTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
			pDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
		}
		// Display-change fix: the surface used to be built once at the
		// startup resolution forever, so switching 1080p -> 1440p (or
		// back) stranded every panel in the old top-left region. Poll
		// the metrics; on a change, rebuild and rescue the layout.
		nsw, _, _ := pGetSystemMetrics.Call(0)
		nsh, _, _ := pGetSystemMetrics.Call(1)
		if int(nsw) != 0 && int(nsh) != 0 && (int(nsw) != ovR.scrW || int(nsh) != ovR.scrH) {
			logLine(fmt.Sprintf("overlay: display changed %dx%d -> %dx%d, rebuilding", ovR.scrW, ovR.scrH, int(nsw), int(nsh)))
			ovRebuildSurface(int(nsw), int(nsh))
		}

		tele.mu.Lock()
		s, live := tele.latest, tele.feedLive
		tele.mu.Unlock()

		edit := ovState.Edit.Load()
		if edit != ovR.editLive {
			style, _, _ := pGetWindowLongPtrW.Call(hwnd, uintptr(gwlExStyle))
			if edit {
				style &^= exTransparent
				// Entering MOVE mode: panels must be on-screen to grab.
				ovClampLayout()
			} else {
				style |= exTransparent
			}
			pSetWindowLongPtrW.Call(hwnd, uintptr(gwlExStyle), style)
			ovR.editLive = edit
		}
		// Session-aware visibility (preview4): outside MOVE mode the
		// overlay renders only while the player is in a session —
		// the gate adds ~1 s to appear on join and ~10 s of
		// continuous absence before hiding, so transitions and
		// dropouts can never blank it near driving. MOVE mode is
		// exempt (panels must be arrangeable from the menus). The
		// flag pill renders inside ovRender, so it inherits this
		// gate automatically. SHOW/HIDE is the manual override for
		// this run only (the overlay arms itself at startup): off
		// means never shown until SHOW is pressed or the app
		// restarts. Preview10: the gate's reading comes from
		// sessionFeed, not raw values — when a session ends, LMU
		// leaves the last session's data frozen in the still-mapped
		// shared memory, and only an advancing session clock proves
		// a LIVE session, so the overlay now leaves the menus alone
		// a bounded time after the session truly ends.
		sessVis, joined, left := ovSessionGate.update(live && ovSessionFeed.update(s, time.Now()), time.Now())
		if joined {
			logLine("overlay: session joined")
		}
		if left {
			logLine("overlay: session left")
		}
		wantShown := edit || (ovState.Shown.Load() && sessVis)
		if wantShown && !ovR.shown {
			if edit {
				pShowWindow.Call(hwnd, swShow)
			} else {
				pShowWindow.Call(hwnd, swShowNoActive)
			}
			ovR.shown = true
		}
		if !wantShown && ovR.shown {
			pShowWindow.Call(hwnd, swHide)
			ovR.shown = false
		}
		if ovR.shown {
			if edit && !live {
				s = placeholderSample()
			}
			ovRender(&s, edit)
			pInvalidateRect.Call(hwnd, 0, 0)
		}
		time.Sleep(33 * time.Millisecond)
	}
}

func fmtLapT(t float64) string {
	if t <= 0 {
		return "--:--.---"
	}
	m := int(t) / 60
	sec := t - float64(m*60)
	return fmt.Sprintf("%d:%06.3f", m, sec)
}

func fmtDelta(d float64) string { return fmt.Sprintf("%+.2f", d) }

// ovRender paints every enabled panel for this frame.
func ovRender(s *Sample, edit bool) {
	dc := ovR.memDC
	W, H := ovR.scrW, ovR.scrH
	ovFill(dc, 0, 0, W, H, colKey)
	for k := range ovR.rects {
		delete(ovR.rects, k)
	}
	mark := func(key string, x, y, w, h int) {
		ovR.rects[key] = rectT{int32(x), int32(y), int32(x + w), int32(y + h)}
		if edit {
			f := ovPanelScale(key)
			S := func(v int) int { return int(float64(v)*f + 0.5) }
			ovFrame(dc, x, y, w, h, colOrange)
			ovFrame(dc, x+1, y+1, w-2, h-2, colOrange)
			ovFill(dc, x, y, S(120), S(18), colSurf2)
			ovText(dc, x+S(4), y+S(2), ovPanelLabels[key], S(11), true, colOrange)
			// resize grip in the bottom-right corner
			gx, gy := x+w-S(20), y+h-S(20)
			ovFill(dc, gx, gy, S(18), S(18), colOrange)
			ovFill(dc, gx+S(4), gy+S(10), S(10), S(2), colDarkInk)
			ovFill(dc, gx+S(10), gy+S(4), S(2), S(10), colDarkInk)
		}
	}
	sc := func(key string) func(int) int {
		f := ovPanelScale(key)
		return func(v int) int { return int(float64(v)*f + 0.5) }
	}

	// ---------- TIMING TOWER, top-left (settings key "relative") ----------
	if ovState.Tower.Load() {
		S := sc("relative")
		stints.update(s.Field)
		secs := TowerWindow(BuildTower(s.Field, s.FieldDents), 3, 3)
		rows := 0
		for _, sec := range secs {
			rows += 1 + len(sec.Rows)
		}
		if edit && len(s.Field) == 0 {
			secs = TowerWindow(BuildTower(placeholderSample().Field, placeholderSample().FieldDents), 3, 3)
			rows = 0
			for _, sec := range secs {
				rows += 1 + len(sec.Rows)
			}
		}
		if rows > 0 {
			// Session context under the title (preview6): track name,
			// session type, time left. In MOVE mode with no live field
			// the placeholder's context stands in, so the block can be
			// arranged at its real height.
			ctxSample := *s
			if edit && len(s.Field) == 0 {
				ctxSample = placeholderSample()
			}
			ctx := towerContextLines(ctxSample)
			dw, dh := 480, 36+len(ctx)*15+rows*21
			ovR.design["relative"] = [2]int{dw, dh}
			w, h := S(dw), S(dh)
			x, y := ovPanelPos("relative", S(24), S(24))
			ovPanel(dc, x, y, w, h)
			ovText(dc, x+S(12), y+S(7), "TOWER", S(12), true, colTeal)
			for i, ln := range ctx {
				ovText(dc, x+S(12), y+S(26)+i*S(15), ln, S(11), false, colMuted)
			}
			ry := y + S(30) + S(len(ctx)*15)
			for _, sec := range secs {
				ovFill(dc, x+S(6), ry, w-S(12), S(18), colSurf2)
				ovText(dc, x+S(12), ry+S(2), sec.Class, S(12), true, colOrange)
				ry += S(21)
				flIdx := classFastestIdx(sec.Rows)
				for ri, r := range sec.Rows {
					if r.Elided {
						ovTextCenter(dc, x+w/2, ry, "···", S(12), false, colMuted)
						ry += S(21)
						continue
					}
					c := r.Car
					col := colPaper
					if c.IsPlayer {
						ovFill(dc, x+S(4), ry-2, w-S(8), S(20), colSurf2)
						col = colOrange
					}
					gap := "LEADER"
					if r.ClassPos > 1 {
						gap = fmt.Sprintf("+%.1f", r.Interval)
					}
					tok := ""
					if c.InPits || c.PitState >= 2 {
						tok = "PIT "
					}
					tok += fmt.Sprintf("ST%d", r.Stint)
					if r.DentSum > 0 {
						tok += fmt.Sprintf(" DMG%d%%", r.DentSum*100/16)
					}
					if c.IsPlayer && s.FuelCapacity > 0 {
						tok += fmt.Sprintf(" FUEL%d%%", int(s.Fuel/s.FuelCapacity*100+0.5))
					}
					name := c.Driver
					if len(name) > 12 {
						name = name[:12]
					}
					// The row is drawn in three segments (same bytes
					// as the old single format string) so the BEST
					// field can turn purple on the fastest-lap row.
					fastest := ri == flIdx
					head := fmt.Sprintf("%-3d %-12s %-7s %-8s ",
						r.ClassPos, name, gap, fmtLapT(c.LastLap))
					bestStr := fmt.Sprintf("%-8s", fmtLapT(c.BestLap))
					bestCol := col
					if fastest {
						bestCol = colPurple
					}
					tx := x + S(12)
					ovTextMono(dc, tx, ry, head, S(13), c.IsPlayer, col)
					bx := tx + ovTextWidth(dc, head, S(13), c.IsPlayer, true)
					ovTextMono(dc, bx, ry, bestStr, S(13), c.IsPlayer, bestCol)
					sx := bx + ovTextWidth(dc, bestStr, S(13), c.IsPlayer, true)
					ovTextMono(dc, sx, ry, " "+tok, S(13), c.IsPlayer, col)
					if fastest {
						ovStopwatch(dc, x+S(6), ry+S(10), S(10), colPurple)
					}
					ry += S(21)
				}
			}
			mark("relative", x, y, w, h)
		}
	}

	// ---------- DELTA, top-right ----------
	deltaH := 124
	if ovState.Delta.Load() {
		S := sc("delta")
		dw, dh := 300, deltaH
		ovR.design["delta"] = [2]int{dw, dh}
		w, h := S(dw), S(dh)
		x, y := ovPanelPos("delta", W-w-S(24), S(92))
		ovPanel(dc, x, y, w, h)
		d, ok := histDelta(s)
		// Bar: zero at centre. The DISPLAYED value follows LMU's own
		// delta widget — the final word on the sign saga, settled by
		// Ari's preview2 field photo (2026-11-02): for the same sector
		// the game showed +3.844 (red) while this panel showed −3.84.
		// histDelta speaks ahead-positive internally; deltaDisplay
		// converts: POSITIVE displayed = losing (red, filling LEFT
		// from centre — the bar's red side), NEGATIVE = gaining
		// (green, filling right). The number sits INSIDE the bar,
		// centred on the zero line, white with a dark outline, no
		// header row.
		disp := deltaDisplay(d)
		bx, by, bw, bh := x+S(10), y+S(8), w-S(20), S(46)
		ovFill(dc, bx, by, bw, bh, colSurf2)
		ovFrame(dc, bx, by, bw, bh, colBorder)
		cx := bx + bw/2
		if ok {
			frac := disp / 2.0 // ±2 s fills a half-bar
			if frac > 1 {
				frac = 1
			}
			if frac < -1 {
				frac = -1
			}
			if frac >= 0 { // losing: red, left of centre
				fw := int(frac * float64(bw/2))
				ovFill(dc, cx-fw, by+1, fw, bh-2, colRed)
			} else { // gaining: green, right of centre
				ovFill(dc, cx, by+1, int(-frac*float64(bw/2)), bh-2, colGreen)
			}
		}
		ovFill(dc, cx, by, 1, bh, colPaper)
		if ok {
			ovTextOutlined(dc, cx, by+S(9), fmtDelta(disp), S(26), true, colPaper)
		} else {
			ovTextCenter(dc, cx, by+S(13), "SET A LAP", S(16), true, colMuted)
		}
		// Sectors / lap count / best+last under the bar. Sector
		// deltas get the same game-convention conversion.
		s1, s2, s3, ok1, ok2, ok3 := histSectors()
		sd := func(v float64, ok bool) string {
			if !ok {
				return "---"
			}
			return fmtDelta(deltaDisplay(v))
		}
		ovTextMono(dc, x+S(12), y+S(60), fmt.Sprintf("S1 %-6s S2 %-6s S3 %-6s", sd(s1, ok1), sd(s2, ok2), sd(s3, ok3)), S(13), false, colPaper)
		lapLine := fmt.Sprintf("LAP %d", s.LapNumber)
		if tot := estTotalLaps(*s); tot > 0 {
			lapLine = fmt.Sprintf("LAP %d/%d", s.LapNumber, tot)
		}
		ovText(dc, x+S(12), y+S(82), lapLine, S(13), true, colOrange)
		ovTextMono(dc, x+S(12), y+S(102), fmt.Sprintf("BEST %s  LAST %s", fmtLapT(histBestLap()), fmtLapT(histLastLap())), S(12), false, colMuted)
		mark("delta", x, y, w, h)
	}

	// ---------- ENGINEER, right side under Delta ----------
	if ovState.Engineer.Load() {
		S := sc("engineer")
		lines := eng.Lines(*s)
		if edit && len(lines) == 0 {
			lines = []string{"FUEL 24L - 6.7 LAPS", "BB 49.0", "WX TRK 31 AIR 24 DRY", "PIT WINDOW OPEN", "EST STOP ≈ 31s - DEFAULT PLAN"}
		}
		if len(lines) > 0 {
			dw, dh := 400, 34+len(lines)*22
			ovR.design["engineer"] = [2]int{dw, dh}
			w, h := S(dw), S(dh)
			x, y := ovPanelPos("engineer", W-w-S(24), S(92+deltaH+14))
			ovPanel(dc, x, y, w, h)
			ovText(dc, x+S(12), y+S(7), "ENGINEER", S(13), true, colTeal)
			for i, ln := range lines {
				col := colPaper
				switch {
				case strings.HasPrefix(ln, "CALL "):
					col = colOrange
				case strings.HasPrefix(ln, "DAMAGE"):
					col = colSoftRed
				case strings.HasPrefix(ln, "EST STOP"), strings.HasPrefix(ln, "STOP EST"), strings.HasPrefix(ln, "IN BOX"):
					col = colTeal
				}
				ovText(dc, x+S(12), y+S(30)+i*S(22), ln, S(14), false, col)
			}
			mark("engineer", x, y, w, h)
		}
	}

	// ---------- TYRES, left-middle (carcass temps — what the game shows) ----------
	if ovState.Tyres.Load() {
		S := sc("tyres")
		dw, dh := 190, 128
		ovR.design["tyres"] = [2]int{dw, dh}
		w, h := S(dw), S(dh)
		x, y := ovPanelPos("tyres", S(24), (H-h)/2)
		ovPanel(dc, x, y, w, h)
		ovText(dc, x+S(10), y+S(6), "TYRES  wear / core °C", S(12), true, colMuted)
		names := [4]string{"FL", "FR", "RL", "RR"}
		for i := 0; i < 4; i++ {
			cx := x + S(10) + (i%2)*S(88)
			cy := y + S(28) + (i/2)*S(50)
			t := s.TyreCarcass[i]
			if t == 0 {
				t = s.TyreTemp[i] // older feed moment: fall back to surface
			}
			if t > 200 {
				t -= 273.15
			}
			ovText(dc, cx, cy, names[i], S(11), true, colOrange)
			ovText(dc, cx, cy+S(15), fmt.Sprintf("%d%%  %.0f°", int(s.TyreWear[i]*100+0.5), t), S(15), true, colPaper)
			ovText(dc, cx, cy+S(33), fmt.Sprintf("%.0f kPa", s.TyrePress[i]), S(10), false, colMuted)
		}
		mark("tyres", x, y, w, h)
	}

	// ---------- INPUTS, bottom centre ----------
	if ovState.Inputs.Load() {
		S := sc("inputs")
		dw, dh := 560, 128
		ovR.design["inputs"] = [2]int{dw, dh}
		w, h := S(dw), S(dh)
		x, y := ovPanelPos("inputs", (W-w)/2, H-h-S(36))
		ovPanel(dc, x, y, w, h)

		// Segmented RPM bar across the top; thresholds self-calibrate
		// from the feed's max RPM (falling back to the highest seen).
		if s.MaxRPM > ovR.maxRPM {
			ovR.maxRPM = s.MaxRPM
		}
		if s.RPM > ovR.maxRPM {
			ovR.maxRPM = s.RPM
		}
		maxR := ovR.maxRPM
		if maxR <= 0 {
			maxR = 8000
		}
		frac := s.RPM / maxR
		if frac > 1 {
			frac = 1
		}
		rx, ry, rw, rh := x+S(12), y+S(10), w-S(24), S(14)
		segs := 28
		segW := rw / segs
		onPit := s.InPits || s.PitState >= 2
		blinkOff := (time.Now().UnixMilli()/300)%2 == 0
		limBlue := rpmLimiterFlash(time.Now().UnixMilli())
		for i := 0; i < segs; i++ {
			sf := float64(i+1) / float64(segs)
			lit := frac >= float64(i)/float64(segs)
			col := colSurf2
			if lit {
				switch {
				case s.SpeedLimiter:
					// Limiter engaged: the lit bar pulses blue/white
					// at ~3 Hz so the held revs are unmissable.
					col = colPaper
					if limBlue {
						col = colLimiter
					}
				case onPit && s.LimiterAvail:
					col = colSurf2 // unlit handled below; lit segments pulse amber
					if !blinkOff {
						col = colPitAmber
					} else {
						col = colAmber
					}
				default:
					switch rpmZone(sf) {
					case 2:
						col = colBrake
					case 1:
						col = colOrange
					default:
						col = colTeal
					}
				}
				// flash the red zone at the shift point
				if !s.SpeedLimiter && rpmZone(sf) == 2 && frac >= 0.97 && blinkOff {
					col = colSurf2
				}
			}
			ovFill(dc, rx+i*segW+1, ry, segW-2, rh, col)
		}

		// Left slot: speed, then the BIG digit — gear normally; on the
		// limiter, pit speed takes the slot with the RPM beneath.
		ovText(dc, x+S(14), y+S(34), fmt.Sprintf("%d", int(s.SpeedKmh+0.5)), S(30), true, colPaper)
		ovText(dc, x+S(14), y+S(72), "km/h", S(11), false, colMuted)
		if s.SpeedLimiter {
			ovText(dc, x+S(78), y+S(34), fmt.Sprintf("%d", int(s.SpeedKmh+0.5)), S(30), true, colLimiter)
			ovText(dc, x+S(78), y+S(72), fmt.Sprintf("PIT · %.0f RPM", s.RPM), S(11), true, colLimiter)
		} else {
			gear := fmt.Sprintf("%d", s.Gear)
			if s.Gear == 0 {
				gear = "N"
			} else if s.Gear < 0 {
				gear = "R"
			}
			ovText(dc, x+S(78), y+S(34), gear, S(30), true, colOrange)
			ovText(dc, x+S(78), y+S(72), fmt.Sprintf("%.0f RPM", s.RPM), S(11), false, colMuted)
		}
		ovText(dc, x+S(14), y+S(102), fmt.Sprintf("LAP %d", s.LapNumber), S(12), true, colOrange)

		// Right column: pedal bars + steering (unchanged behaviour).
		rx0 := x + w - S(112)
		bw, bh := S(14), S(48)
		byy := y + S(34)
		ovFill(dc, rx0, byy, bw, bh, colSurf2)
		ovFill(dc, rx0, byy+int((1-s.Brake)*float64(bh)), bw, int(s.Brake*float64(bh)), colBrake)
		ovFill(dc, rx0+S(22), byy, bw, bh, colSurf2)
		ovFill(dc, rx0+S(22), byy+int((1-s.Throttle)*float64(bh)), bw, int(s.Throttle*float64(bh)), colTeal)
		ovText(dc, rx0+S(44), byy+S(2), "BRK", S(10), true, colBrake)
		ovText(dc, rx0+S(44), byy+S(16), "THR", S(10), true, colTeal)
		ovText(dc, rx0+S(44), byy+S(30), "STR", S(10), true, colMuted)
		sx, sy, sw, sh := rx0, y+S(102), S(98), S(10)
		ovFill(dc, sx, sy, sw, sh, colSurf2)
		ovFill(dc, sx+sw/2-1, sy-2, 2, sh+4, colMuted)
		st := math.Max(-1, math.Min(1, s.Steering))
		if st >= 0 {
			ovFill(dc, sx+sw/2, sy, int(st*float64(sw/2)), sh, colPaper)
		} else {
			ovFill(dc, sx+sw/2+int(st*float64(sw/2)), sy, int(-st*float64(sw/2)), sh, colPaper)
		}
		// Combined ribbon: throttle teal under, brake red over.
		rx2, rw2 := x+S(150), w-S(290)
		ry2, rh2 := y+S(34), S(80)
		ovFill(dc, rx2, ry2, rw2, rh2, colSurf2)
		ovFill(dc, rx2, ry2+rh2/2, rw2, 1, colBorder)
		rec := histRecent(420)
		brk := make([]float64, len(rec))
		thr := make([]float64, len(rec))
		for i, r := range rec {
			brk[i], thr[i] = r.Brake, r.Throttle
		}
		ovRibbon(dc, thr, rx2+2, ry2+2, rw2-4, rh2-4, colTeal)
		ovRibbon(dc, brk, rx2+2, ry2+2, rw2-4, rh2-4, colBrake)
		mark("inputs", x, y, w, h)
	}

	// ---------- FLAG PILL, top-centre under the virtual mirror ----------
	// Master switch (preview3): with the Flags toggle off the pill
	// never renders and the tracker isn't even consulted. While on,
	// every change of the DISPLAYED state is written to the app log
	// with its source (game vs hazard) — the evidence for whether
	// disputed yellows come from LMU's sector flags or our own
	// stopped-car leg.
	if ovState.Flags.Load() {
		pill, active := flags.Pill(*s)
		if line := flags.LogLine(pill, active); line != "" {
			logLine(line)
		}
		if active {
			px := 13
			w := len(pill.Text)*8 + 30
			h := 30
			x, y := (W-w)/2, 56
			switch pill.Kind {
			case "green":
				ovFill(dc, x, y, w, h, colGreen)
				ovTextCenter(dc, x+w/2, y+6, pill.Text, px, true, colPaper)
			case "yellow":
				ovFill(dc, x, y, w, h, colAmber)
				ovTextCenter(dc, x+w/2, y+6, pill.Text, px, true, colDarkInk)
			case "blue":
				ovFill(dc, x, y, w, h, colBlue)
				ovTextCenter(dc, x+w/2, y+6, pill.Text, px, true, colPaper)
			}
			ovFrame(dc, x, y, w, h, colBorder)
		}
	}
}
