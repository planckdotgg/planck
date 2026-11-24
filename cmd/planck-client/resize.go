package main

import (
	"sync"
	"time"
)

// resizeSettle is how long a window's size must hold still before the
// stream follows it: a drag sends one resize, at its end.
const resizeSettle = 300 * time.Millisecond

// viewResizer passes the stream window's settled size in pixels to send:
// delay after the last change, or at once on entering or leaving full
// screen, whose size is final.
type viewResizer struct {
	delay time.Duration
	send  func(w, h int)

	mu    sync.Mutex
	timer *time.Timer
	w, h  int
}

// changed notes a size during a resize; send hears the last one once they
// stop for delay.
func (r *viewResizer) changed(w, h int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.w, r.h = w, h
	if r.timer == nil {
		r.timer = time.AfterFunc(r.delay, r.fire)
		return
	}
	r.timer.Reset(r.delay)
}

// now sends w x h at once, in place of any size still settling.
func (r *viewResizer) now(w, h int) {
	r.mu.Lock()
	r.w, r.h = w, h
	if r.timer != nil {
		r.timer.Stop()
	}
	r.mu.Unlock()
	r.send(w, h)
}

func (r *viewResizer) fire() {
	r.mu.Lock()
	w, h := r.w, r.h
	r.mu.Unlock()
	r.send(w, h)
}
