//go:build windows

package main

// The in-game overlay: one transparent, always-on-top window covering the
// primary display, painted with plain GDI. Panels draw only while the
// telemetry feed is live (or in MOVE mode), each panel is individually
// toggleable, panels can be dragged in MOVE mode, and a global size scale
// applies to all of them. Requires the game in borderless/windowed mode —
// nothing can draw over exclusive fullscreen.

import (
	"fmt"
	"runtime"
	"syscall"
	"time"
	"unsafe"
)

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	gdi32    = syscall.NewLazyDLL("gdi32.dll")
	ovKernel = syscall.NewLazyDLL("kernel32.dll")

	pRegisterClassExW   = user32.NewProc("RegisterClassExW")
	pCreateWindowExW    = user32.NewProc("CreateWindowExW")
	pDefWindowProcW     = user32.NewProc("DefWindowProcW")
	pPeekMessageW       = user32.NewProc("PeekMessageW")
	pTranslateMessage   = user32.NewProc("TranslateMessage")
	pDispatchMessageW   = user32.NewProc("DispatchMessageW")
	pBeginPaint         = user32.NewProc("BeginPaint")
	pEndPaint           = user32.NewProc("EndPaint")
	pInvalidateRect     = user32.NewProc("InvalidateRect")
	pShowWindow         = user32.NewProc("ShowWindow")
	pGetSystemMetrics   = user32.NewProc("GetSystemMetrics")
	pSetLayeredWinAttr  = user32.NewProc("SetLayeredWindowAttributes")
	pGetWindowLongPtrW  = user32.NewProc("GetWindowLongPtrW")
	pSetWindowLongPtrW  = user32.NewProc("SetWindowLongPtrW")
	pGetDC              = user32.NewProc("GetDC")
	pReleaseDC          = user32.NewProc("ReleaseDC")
	pFillRect           = user32.NewProc("FillRect")

	pCreateCompatibleDC     = gdi32.NewProc("CreateCompatibleDC")
	pCreateCompatibleBitmap = gdi32.NewProc("CreateCompatibleBitmap")
	pSelectObject           = gdi32.NewProc("SelectObject")
	pDeleteDC               = gdi32.NewProc("DeleteDC")
	pDeleteObject           = gdi32.NewProc("DeleteObject")
	pBitBlt                 = gdi32.NewProc("BitBlt")
	pCreateSolidBrush       = gdi32.NewProc("CreateSolidBrush")
	pCreatePen              = gdi32.NewProc("CreatePen")
	pRectangle              = gdi32.NewProc("Rectangle")
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
	wsPopup        = 0x80000000
	exLayered      = 0x00080000
	exTransparent  = 0x00000020
	exTopmost      = 0x00000008
	exNoActivate   = 0x08000000
	exToolWindow   = 0x00000080
	lwaColorkey    = 0x00000001
	swHide         = 0
	swShow         = 5
	swShowNoActive = 4
	wmPaint        = 0x000F
	wmLButtonDown  = 0x0201
	wmMouseMove    = 0x0200
	wmLButtonUp    = 0x0202
	srccopy        = 0x00CC0020
	transparentBk  = 1
	psSolid        = 0
)

var gwlExStyle = int32(-20) // GWL_EXSTYLE, signed on purpose

func cref(r, g, b uint32) uintptr { return uintptr(r | g<<8 | b<<16) }

var (
	colKey     = cref(255, 0, 255)
	colSurface = cref(0x11, 0x1d, 0x33)
	colSurf2   = cref(0x17, 0x2a, 0x4f)
	colBorder  = cref(0x2b, 0x36, 0x48)
	colPaper   = cref(0xf7, 0xf7, 0xff)
	colMuted   = cref(0xa8, 0xbc, 0xd6)
	colOrange  = cref(0xff, 0x9f, 0x1c)
	colTeal    = cref(0x2e, 0xc4, 0xb6)
	colRed     = cref(0xd7, 0x26, 0x3d)
	colGreen   = cref(0x1f, 0xa3, 0x96)
	colBrake   = cref(0xff, 0x33, 0x55)
)

// saveOverlayLayout is set by main.go to persist dragged positions / scale.
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
	dragKey  string
	dragDX   int
	dragDY   int
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

func ovFont(px int, bold bool) uintptr {
	if ovR.fonts == nil {
		ovR.fonts = map[int]uintptr{}
	}
	key := px
	if bold {
		key = -px
	}
	if f, ok := ovR.fonts[key]; ok {
		return f
	}
	weight := uintptr(400)
	if bold {
		weight = 700
	}
	face, _ := syscall.UTF16PtrFromString("Segoe UI")
	f, _, _ := pCreateFontW.Call(uintptr(-px), 0, 0, 0, weight, 0, 0, 0, 0, 0, 0, 0, 0, uintptr(unsafe.Pointer(face)))
	ovR.fonts[key] = f
	return f
}

type rectT struct{ L, T, R, B int32 }

func ovFill(dc uintptr, x, y, w, h int, color uintptr) {
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
	old, _, _ := pSelectObject.Call(dc, ovFont(px, bold))
	pSetTextColor.Call(dc, color)
	u16, _ := syscall.UTF16FromString(s)
	pTextOutW.Call(dc, uintptr(x), uintptr(y), uintptr(unsafe.Pointer(&u16[0])), uintptr(len(u16)-1))
	pSelectObject.Call(dc, old)
}

func ovTextCenter(dc uintptr, cx, y int, s string, px int, bold bool, color uintptr) {
	old, _, _ := pSelectObject.Call(dc, ovFont(px, bold))
	var sz struct{ Cx, Cy int32 }
	u16, _ := syscall.UTF16FromString(s)
	pGetTextExtentPoint32W.Call(dc, uintptr(unsafe.Pointer(&u16[0])), uintptr(len(u16)-1), uintptr(unsafe.Pointer(&sz)))
	pSetTextColor.Call(dc, color)
	pTextOutW.Call(dc, uintptr(cx-int(sz.Cx)/2), uintptr(y), uintptr(unsafe.Pointer(&u16[0])), uintptr(len(u16)-1))
	pSelectObject.Call(dc, old)
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

func ovHitTest(x, y int) string {
	// Reverse draw order: what is painted last sits on top.
	for _, key := range []string{"relative", "fuel", "tyres", "inputs", "delta", "engineer"} {
		if r, ok := ovR.rects[key]; ok {
			if x >= int(r.L) && x < int(r.R) && y >= int(r.T) && y < int(r.B) {
				return key
			}
		}
	}
	return ""
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
			if key := ovHitTest(x, y); key != "" {
				if r, ok := ovR.rects[key]; ok {
					ovR.dragKey = key
					ovR.dragDX = x - int(r.L)
					ovR.dragDY = y - int(r.T)
				}
			}
			return 0
		}
	case wmMouseMove:
		if ovR.dragKey != "" {
			x := int(int16(lParam & 0xFFFF))
			y := int(int16((lParam >> 16) & 0xFFFF))
			ovSetPanelPos(ovR.dragKey, x-ovR.dragDX, y-ovR.dragDY)
			return 0
		}
	case wmLButtonUp:
		if ovR.dragKey != "" {
			ovR.dragKey = ""
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
		SpeedKmh: 167, Gear: 3, LapNumber: 4, Throttle: 0.35, Brake: 0.8, Fuel: 38.2,
		TyreWear: [4]float64{0.95, 0.94, 0.96, 0.95},
		TyreTemp: [4]float64{82, 84, 80, 81},
		Field: []CarInfo{
			{Place: 11, Driver: "AHEAD 1", BehindLeader: 40.0},
			{Place: 12, Driver: "AHEAD 2", BehindLeader: 42.6},
			{Place: 13, Driver: "YOU", BehindLeader: 43.5, IsPlayer: true},
			{Place: 14, Driver: "BEHIND 1", BehindLeader: 44.8},
			{Place: 15, Driver: "BEHIND 2", BehindLeader: 47.6},
		},
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
	ovR.scrW, ovR.scrH = int(sw), int(sh)
	if ovR.scrW == 0 {
		logLine("overlay: no screen metrics")
		return
	}
	ovR.rects = map[string]rectT{}

	hInst, _, _ := pGetModuleHandleW.Call(0)
	className, _ := syscall.UTF16PtrFromString("JettOverlayWindow")
	wc := wndClassEx{WndProc: ovWndProc, Instance: hInst, ClassName: className}
	wc.Size = uint32(unsafe.Sizeof(wc))
	pRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))

	hwnd, _, _ := pCreateWindowExW.Call(
		exLayered|exTransparent|exTopmost|exNoActivate|exToolWindow,
		uintptr(unsafe.Pointer(className)),
		0, wsPopup,
		0, 0, uintptr(ovR.scrW), uintptr(ovR.scrH),
		0, 0, hInst, 0)
	if hwnd == 0 {
		logLine("overlay: CreateWindowEx failed")
		return
	}
	ovR.hwnd = hwnd
	pSetLayeredWinAttr.Call(hwnd, colKey, 0, lwaColorkey)

	dc0, _, _ := pGetDC.Call(0)
	memDC, _, _ := pCreateCompatibleDC.Call(dc0)
	bmp, _, _ := pCreateCompatibleBitmap.Call(dc0, uintptr(ovR.scrW), uintptr(ovR.scrH))
	old, _, _ := pSelectObject.Call(memDC, bmp)
	ovR.memDC, ovR.bitmap, ovR.oldBmp = memDC, bmp, old
	pReleaseDC.Call(0, dc0)
	pSetBkMode.Call(memDC, transparentBk)

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
		tele.mu.Lock()
		s, live := tele.latest, tele.feedLive
		tele.mu.Unlock()

		edit := ovState.Edit.Load()
		if edit != ovR.editLive {
			style, _, _ := pGetWindowLongPtrW.Call(hwnd, uintptr(gwlExStyle))
			if edit {
				style &^= exTransparent
			} else {
				style |= exTransparent
			}
			pSetWindowLongPtrW.Call(hwnd, uintptr(gwlExStyle), style)
			ovR.editLive = edit
		}
		wantShown := edit || (ovState.Shown.Load() && live)
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

func ovRender(s *Sample, edit bool) {
	dc := ovR.memDC
	W, H := ovR.scrW, ovR.scrH
	scf := ovScaleVal()
	S := func(v int) int { return int(float64(v)*scf + 0.5) }
	ovFill(dc, 0, 0, W, H, colKey)
	for k := range ovR.rects {
		delete(ovR.rects, k)
	}
	mark := func(key string, x, y, w, h int) {
		ovR.rects[key] = rectT{int32(x), int32(y), int32(x + w), int32(y + h)}
		if edit {
			ovFrame(dc, x, y, w, h, colOrange)
			ovFrame(dc, x+1, y+1, w-2, h-2, colOrange)
			ovFill(dc, x, y, S(66), S(18), colSurf2)
			ovText(dc, x+S(4), y+S(2), key, S(11), true, colOrange)
		}
	}

	// ---- delta, top centre — below the virtual mirror by default ----
	if ovState.Delta.Load() {
		w, h := S(320), S(46)
		x, y := ovPanelPos("delta", (W-w)/2, 150)
		d, ok := histDelta(s)
		if edit && !ok {
			ovFill(dc, x, y, w, h, colSurface)
			ovTextCenter(dc, x+w/2, y+S(11), "-0.42", S(18), true, colMuted)
		} else if ok {
			bg := colGreen
			if d < 0 {
				bg = colRed
			}
			ovFill(dc, x, y, w, h, bg)
			ovTextCenter(dc, x+w/2, y+S(7), fmt.Sprintf("%+.2f", -d), S(26), true, colPaper)
		} else {
			ovFill(dc, x, y, w, h, colSurface)
			ovFrame(dc, x, y, w, h, colBorder)
			ovTextCenter(dc, x+w/2, y+S(11), "SET A LAP", S(18), true, colMuted)
		}
		mark("delta", x, y, w, h)
	}

	// ---- inputs, bottom centre: pedals overlapped in ONE ribbon ----
	if ovState.Inputs.Load() {
		w, h := S(560), S(104)
		x, y := ovPanelPos("inputs", (W-w)/2, H-h-S(40))
		ovFill(dc, x, y, w, h, colSurface)
		ovFrame(dc, x, y, w, h, colBorder)
		ovText(dc, x+S(14), y+S(10), fmt.Sprintf("%d", int(s.SpeedKmh+0.5)), S(30), true, colPaper)
		ovText(dc, x+S(14), y+S(48), fmt.Sprintf("km/h  G%d", s.Gear), S(12), false, colMuted)
		ovText(dc, x+S(14), y+S(68), fmt.Sprintf("LAP %d", s.LapNumber), S(12), true, colOrange)
		// right column: pedal bars on top, steering bar underneath
		rx0 := x + w - S(112)
		bw, bh := S(14), S(48)
		ovFill(dc, rx0, y+S(12), bw, bh, colSurf2)
		ovFill(dc, rx0, y+S(12)+int((1-s.Brake)*float64(bh)), bw, int(s.Brake*float64(bh)), colBrake)
		ovFill(dc, rx0+S(22), y+S(12), bw, bh, colSurf2)
		ovFill(dc, rx0+S(22), y+S(12)+int((1-s.Throttle)*float64(bh)), bw, int(s.Throttle*float64(bh)), colTeal)
		ovText(dc, rx0+S(44), y+S(14), "BRK", S(10), true, colBrake)
		ovText(dc, rx0+S(44), y+S(28), "THR", S(10), true, colTeal)
		ovText(dc, rx0+S(44), y+S(42), "STR", S(10), true, colMuted)
		// steering: horizontal bar, centre = straight ahead
		sx, sy, sw, sh := rx0, y+S(78), S(98), S(10)
		ovFill(dc, sx, sy, sw, sh, colSurf2)
		ovFill(dc, sx+sw/2-1, sy-2, 2, sh+4, colMuted)
		st := s.Steering
		if st < -1 {
			st = -1
		}
		if st > 1 {
			st = 1
		}
		if st >= 0 {
			ovFill(dc, sx+sw/2, sy, int(st*float64(sw/2)), sh, colPaper)
		} else {
			ovFill(dc, sx+sw/2+int(st*float64(sw/2)), sy, int(-st*float64(sw/2)), sh, colPaper)
		}
		// combined ribbon: throttle teal under, brake red over
		rx, rw := x+S(112), w-S(240)
		ry, rh := y+S(12), S(80)
		ovFill(dc, rx, ry, rw, rh, colSurf2)
		ovFill(dc, rx, ry+rh/2, rw, 1, colBorder)
		rec := histRecent(420)
		brk := make([]float64, len(rec))
		thr := make([]float64, len(rec))
		for i, r := range rec {
			brk[i], thr[i] = r.Brake, r.Throttle
		}
		ovRibbon(dc, thr, rx+2, ry+2, rw-4, rh-4, colTeal)
		ovRibbon(dc, brk, rx+2, ry+2, rw-4, rh-4, colBrake)
		mark("inputs", x, y, w, h)
	}

	// ---- tyres, bottom left ----
	if ovState.Tyres.Load() {
		w, h := S(178), S(122)
		x, y := ovPanelPos("tyres", S(24), H-h-S(24))
		ovFill(dc, x, y, w, h, colSurface)
		ovFrame(dc, x, y, w, h, colBorder)
		ovText(dc, x+S(10), y+S(6), "TYRES  wear / °C", S(12), true, colMuted)
		names := [4]string{"FL", "FR", "RL", "RR"}
		for i := 0; i < 4; i++ {
			cx := x + S(10) + (i%2)*S(82)
			cy := y + S(28) + (i/2)*S(46)
			t := s.TyreTemp[i]
			if t > 200 {
				t -= 273.15
			}
			ovText(dc, cx, cy, names[i], S(11), true, colOrange)
			ovText(dc, cx, cy+S(15), fmt.Sprintf("%d%%  %.0f°", int(s.TyreWear[i]*100+0.5), t), S(14), true, colPaper)
		}
		mark("tyres", x, y, w, h)
	}

	// ---- fuel, bottom right ----
	if ovState.Fuel.Load() {
		w, h := S(240), S(74)
		x, y := ovPanelPos("fuel", W-w-S(24), H-h-S(24))
		ovFill(dc, x, y, w, h, colSurface)
		ovFrame(dc, x, y, w, h, colBorder)
		ovText(dc, x+S(12), y+S(8), fmt.Sprintf("FUEL  %.1f L", s.Fuel), S(18), true, colPaper)
		pl := histFuelPerLap()
		if pl > 0 {
			ovText(dc, x+S(12), y+S(38), fmt.Sprintf("%.2f L/lap · ~%d laps left", pl, int(s.Fuel/pl)), S(13), false, colMuted)
		} else {
			ovText(dc, x+S(12), y+S(38), "per-lap use: learning…", S(13), false, colMuted)
		}
		mark("fuel", x, y, w, h)
	}

	// ---- engineer, left centre: strategy as text ----
	if ovState.Engineer.Load() {
		lines := eng.Lines(*s)
		if edit && len(lines) == 0 {
			lines = []string{"FUEL 24L - 6.7 LAPS", "PIT WINDOW OPEN", "STOP EST ~31s"}
		}
		if len(lines) > 0 {
			w := S(380)
			h := S(34) + len(lines)*S(22)
			x, y := ovPanelPos("engineer", S(24), (H-h)/2)
			ovFill(dc, x, y, w, h, colSurface)
			ovFrame(dc, x, y, w, h, colTeal)
			ovText(dc, x+S(12), y+S(7), "ENGINEER", S(13), true, colTeal)
			for i, ln := range lines {
				ovText(dc, x+S(12), y+S(30)+i*S(22), ln, S(14), false, colPaper)
			}
			mark("engineer", x, y, w, h)
		}
	}

	// ---- relative, right centre ----
	if ovState.Relative.Load() {
		rows := RelativeRows(s.Field, 3)
		if len(rows) > 0 || edit {
			if len(rows) == 0 {
				rows = RelativeRows(placeholderSample().Field, 3)
			}
			w := S(268)
			h := len(rows)*S(24) + S(14)
			x, y := ovPanelPos("relative", W-w-S(24), (H-h)/2)
			ovFill(dc, x, y, w, h, colSurface)
			ovFrame(dc, x, y, w, h, colBorder)
			for i, r := range rows {
				ry := y + S(7) + i*S(24)
				col := colPaper
				if r.Player {
					ovFill(dc, x+3, ry-2, w-6, S(22), colSurf2)
					col = colOrange
				}
				gap := "  YOU"
				if !r.Player {
					gap = fmt.Sprintf("%+.1f", r.Gap)
				}
				name := r.Driver
				if len(name) > 12 {
					name = name[:12]
				}
				ovText(dc, x+S(10), ry, fmt.Sprintf("P%-3d %-7s %s", r.Place, gap, name), S(14), r.Player, col)
			}
			mark("relative", x, y, w, h)
		}
	}
}
