package main

import (
	"github.com/ebitengine/purego/objc"

	"github.com/planckdotgg/planck/pkg/input"
)

// NSEvent types of the presses and releases of the buttons past the right.
const otherMouseEvents = 1<<25 | 1<<26

// macButtons reads the buttons past the right from AppKit: MyGo reports
// each of them as its button 2, middle, so a local monitor, which sees an
// event just before the window does, keeps its buttonNumber.
type macButtons struct {
	monitor objc.ID
	// number is the buttonNumber of the last press or release past the
	// right; 2 until one comes.
	number int64
}

func newButtonSys(pointerSys, func(input.Button, bool)) buttonSys {
	return &macButtons{number: 2}
}

func (m *macButtons) start() {
	blk := objc.NewBlock(func(_ objc.Block, ev objc.ID) objc.ID {
		m.number = objc.Send[int64](ev, objc.RegisterName("buttonNumber"))
		return ev
	})
	m.monitor = objc.ID(objc.GetClass("NSEvent")).Send(objc.RegisterName("addLocalMonitorForEventsMatchingMask:handler:"), uint64(otherMouseEvents), blk).
		Send(objc.RegisterName("retain"))
}

func (m *macButtons) other() (input.Button, bool) { return otherButton(m.number) }

// otherButton is the host's button for AppKit's button number n.
func otherButton(n int64) (input.Button, bool) {
	switch n {
	case 2:
		return input.ButtonMiddle, true
	case 3:
		return input.ButtonX1, true
	case 4:
		return input.ButtonX2, true
	}
	return 0, false
}

func (m *macButtons) down(b input.Button) bool {
	held := objc.Send[uint64](objc.ID(objc.GetClass("NSEvent")), objc.RegisterName("pressedMouseButtons"))
	return held&(1<<(b-input.ButtonLeft)) != 0
}
