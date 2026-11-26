//go:build linux && !android && (amd64 || arm64)

package main

import "github.com/planckdotgg/planck/pkg/input"

// linuxButtons reads the side buttons, which GTK hands MyGo as Back and
// Forward keys, and every button's state, from the window's seat.
type linuxButtons struct {
	p    *linuxPointer
	side func(input.Button, bool)
}

func newButtonSys(p pointerSys, side func(input.Button, bool)) buttonSys {
	lp, ok := p.(*linuxPointer)
	if !ok {
		return nil
	}
	return &linuxButtons{p: lp, side: side}
}

// start runs on the main thread as the stream view first builds; a
// window made later gets a seat of its own (seatFor), which keeps the
// listener.
func (b *linuxButtons) start() {
	if s := b.p.seat(); s != nil {
		f := b.side
		s.side.Store(&f)
	}
}

// other is MyGo's button 2 on Linux: the middle one.
func (b *linuxButtons) other() (input.Button, bool) { return input.ButtonMiddle, true }

func (b *linuxButtons) down(btn input.Button) bool {
	seats.mu.Lock()
	s := seats.cur
	seats.mu.Unlock()
	return s != nil && s.buttons.Load()&(1<<btn) != 0
}
