package main

import (
	"sync"
	"sync/atomic"
)

// Overlay panel switches, shared between the app window (checkboxes) and
// the overlay renderer. Every panel is individually toggleable — off means
// zero drawing cost, which matters on a GPU Ari wants to protect.
var ovState struct {
	Shown    atomic.Bool
	Inputs   atomic.Bool
	Delta    atomic.Bool
	Relative atomic.Bool
	Tyres    atomic.Bool
	Fuel     atomic.Bool
	Engineer atomic.Bool
	Edit     atomic.Bool // MOVE mode: window takes mouse input for dragging
}

// Panel layout: user-dragged top-left positions (screen px) and a global
// size scale. A missing position means "use the default anchor".
var ovLayout struct {
	mu    sync.Mutex
	pos   map[string][2]int
	scale float64
}

func ovScaleVal() float64 {
	ovLayout.mu.Lock()
	defer ovLayout.mu.Unlock()
	if ovLayout.scale <= 0 {
		return 1.0
	}
	return ovLayout.scale
}

func ovSetScale(f float64) {
	ovLayout.mu.Lock()
	ovLayout.scale = f
	ovLayout.mu.Unlock()
}

// ovPanelPos returns the panel's top-left: the user's dragged spot when one
// is saved, otherwise the supplied default anchor.
func ovPanelPos(key string, defX, defY int) (int, int) {
	ovLayout.mu.Lock()
	defer ovLayout.mu.Unlock()
	if p, ok := ovLayout.pos[key]; ok {
		return p[0], p[1]
	}
	return defX, defY
}

func ovSetPanelPos(key string, x, y int) {
	ovLayout.mu.Lock()
	if ovLayout.pos == nil {
		ovLayout.pos = map[string][2]int{}
	}
	ovLayout.pos[key] = [2]int{x, y}
	ovLayout.mu.Unlock()
}

// overlayInit applies saved preferences (or sensible defaults) at startup.
// Done once in main() before the overlay loop starts, so no sync needed.
func overlayInit(on bool, p map[string]bool, pos map[string][]int, scale float64) {
	ovState.Shown.Store(on)
	ovLayout.mu.Lock()
	ovLayout.scale = scale
	ovLayout.pos = map[string][2]int{}
	for k, v := range pos {
		if len(v) == 2 {
			ovLayout.pos[k] = [2]int{v[0], v[1]}
		}
	}
	ovLayout.mu.Unlock()
	if p == nil {
		ovState.Inputs.Store(true)
		ovState.Delta.Store(true)
		ovState.Relative.Store(true)
		ovState.Tyres.Store(true)
		ovState.Fuel.Store(true)
		ovState.Engineer.Store(true)
		return
	}
	ovState.Inputs.Store(p["inputs"])
	ovState.Delta.Store(p["delta"])
	ovState.Relative.Store(p["relative"])
	ovState.Tyres.Store(p["tyres"])
	ovState.Fuel.Store(p["fuel"])
	ovState.Engineer.Store(p["engineer"])
}

func overlayConfig() (bool, map[string]bool, map[string][]int, float64) {
	ovLayout.mu.Lock()
	pos := map[string][]int{}
	for k, v := range ovLayout.pos {
		pos[k] = []int{v[0], v[1]}
	}
	scale := ovLayout.scale
	ovLayout.mu.Unlock()
	return ovState.Shown.Load(), map[string]bool{
		"inputs":   ovState.Inputs.Load(),
		"delta":    ovState.Delta.Load(),
		"relative": ovState.Relative.Load(),
		"tyres":    ovState.Tyres.Load(),
		"fuel":     ovState.Fuel.Load(),
		"engineer": ovState.Engineer.Load(),
	}, pos, scale
}
