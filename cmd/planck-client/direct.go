//go:build darwin || windows

package main

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"

	"github.com/planckdotgg/planck/pkg/defaults"
	"github.com/planckdotgg/planck/pkg/player"
)

// directSettle is the least time between two swaps of the present path,
// each of which costs a frame of pacing.
const directSettle = 250 * time.Millisecond

// directMode decides when the stream's surface scans out directly. In
// direct mode MyGo's layers stop compositing, so nothing of the overlay
// would show: the surface goes direct only while the overlay draws
// nothing, and comes back to the compositor as soon as it draws again.
type directMode struct {
	allowed atomic.Bool
	overlay atomic.Bool

	mu   sync.Mutex
	gen  uint64
	last time.Time
}

func directWanted(allowed, fullScreen, overlay bool) bool { return allowed && fullScreen && !overlay }

// directDelay is how long a swap waits: the overlay coming back swaps at
// once, and going direct waits directSettle, so that a menu closed and
// opened again swaps nothing; neither comes within directSettle of the
// last swap.
func directDelay(toDirect bool, now, last time.Time) time.Duration {
	wait := directSettle - now.Sub(last)
	if toDirect {
		wait = max(wait, directSettle)
	}
	return max(wait, 0)
}

// overlayChanged hears the stream overlay start or stop drawing. It runs
// inside the UI's frame, so the surface changes after it, on the main
// thread.
func (v *videoView) overlayChanged(shown bool) {
	v.mode.overlay.Store(shown)
	m := &v.mode
	m.mu.Lock()
	m.gen++
	gen, d := m.gen, directDelay(!shown, time.Now(), m.last)
	m.mu.Unlock()
	time.AfterFunc(d, func() {
		m.mu.Lock()
		current := m.gen == gen
		m.mu.Unlock()
		if current {
			mygo.RunOnMain(v.applyDirect)
		}
	})
}

// applyDirect runs on the main thread. A swap shows the picture again in
// the drawables' new size, and asks for a keyframe, so the new present
// path starts on one.
func (v *videoView) applyDirect() {
	w, s := v.win.Load(), v.cur.Load()
	if w == nil || s == nil {
		return
	}
	h := s.host
	want := directWanted(v.mode.allowed.Load(), w.IsFullScreen(), v.mode.overlay.Load())
	if h.surf.Direct() == want {
		return
	}
	if err := h.surf.SetDirect(want); err != nil {
		v.logf("planck: direct display %v: %v", want, err)
		v.mode.allowed.Store(false)
		return
	}
	v.mode.mu.Lock()
	v.mode.last = time.Now()
	v.mode.mu.Unlock()
	v.drawable.Store(packSize(h.surf.Size()))
	v.refit()
	if v.keyframe != nil {
		v.keyframe()
	}
	v.logf("planck: direct display %v", want)
}

// SetProfile takes the session's resolved profile: whether direct display
// is allowed, and the frame pacing.
func (s *surfaceSink) SetProfile(p *defaults.ResolvedProfile) {
	pacing := player.PaceLowestLatency
	if p.VSync {
		pacing = player.PaceSmooth
	}
	s.p.SetPacing(pacing)
	s.v.mode.allowed.Store(p.DirectDisplay)
	mygo.RunOnMain(s.v.applyDirect)
}
