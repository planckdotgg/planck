package main

import "github.com/planckdotgg/planck/pkg/input"

var procGetSystemMetrics = user32.NewProc("GetSystemMetrics")

const smSwapButton = 23

// winButtons reads the side buttons, which the window hears as Back and
// Forward alone, from the pointer's raw input.
type winButtons struct{ raw *rawMouse }

func newButtonSys(p pointerSys, side func(input.Button, bool)) buttonSys {
	wp, ok := p.(*winPointer)
	if !ok {
		return nil
	}
	wp.raw.buttons = side
	return &winButtons{raw: &wp.raw}
}

func (w *winButtons) start() { w.raw.start() }

func (w *winButtons) other() (input.Button, bool) { return input.ButtonMiddle, true }

// down reads the button's state now. GetAsyncKeyState reads the physical
// buttons, the window's left being the physical right when swapped.
func (w *winButtons) down(b input.Button) bool {
	vk := map[input.Button]uintptr{input.ButtonLeft: 0x01, input.ButtonRight: 0x02, input.ButtonMiddle: 0x04, input.ButtonX1: 0x05, input.ButtonX2: 0x06}[b]
	if b == input.ButtonLeft || b == input.ButtonRight {
		if swapped, _, _ := procGetSystemMetrics.Call(smSwapButton); swapped != 0 {
			vk ^= 0x03
		}
	}
	r, _, _ := procGetAsyncKeyState.Call(vk)
	return r&0x8000 != 0
}
