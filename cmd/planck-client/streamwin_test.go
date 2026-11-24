package main

import (
	"slices"
	"testing"
	"time"
)

// fakeNativeWin is a window whose full-screen transitions end when the
// test says so, as macOS's do; sync ends them at once, as Windows' do.
type fakeNativeWin struct {
	m          *streamWindows
	sync       bool
	full       bool
	transition bool
	titles     []string
	focuses    int
	fullCalls  []bool
	destroyed  bool
}

func (w *fakeNativeWin) IsFullScreen() bool { return w.full }
func (w *fakeNativeWin) SetFullScreen(on bool) {
	w.fullCalls = append(w.fullCalls, on)
	if on == w.full || w.transition {
		return
	}
	w.full = on
	w.transition = true
	if w.sync {
		w.endTransition()
	}
}
func (w *fakeNativeWin) endTransition() {
	w.transition = false
	w.m.fullScreenChanged(w.full)
}
func (w *fakeNativeWin) SetTitle(t string) { w.titles = append(w.titles, t) }
func (w *fakeNativeWin) Focus()            { w.focuses++ }
func (w *fakeNativeWin) Destroy()          { w.destroyed = true }

type delayed struct {
	d  time.Duration
	fn func()
}

// streamWinRig is a streamWindows with a main loop the test turns.
type streamWinRig struct {
	m        *streamWindows
	wins     []*fakeNativeWin
	full     bool
	sync     bool
	video    bool
	queue    []delayed
	userEnds int
	gones    int
}

func newStreamWinRig(full, sync bool) *streamWinRig {
	r := &streamWinRig{full: full, sync: sync}
	r.m = &streamWindows{
		create: func(title string) nativeStreamWin {
			w := &fakeNativeWin{m: r.m, sync: r.sync, titles: []string{title}}
			r.wins = append(r.wins, w)
			return w
		},
		fullscreen: func() bool { return r.full },
		videoLive:  func() bool { return r.video },
		userClosed: func() { r.userEnds++ },
		gone:       func() { r.gones++ },
		later:      func(d time.Duration, fn func()) { r.queue = append(r.queue, delayed{d, fn}) },
	}
	return r
}

// turn runs what the main loop has queued now, not the timeouts.
func (r *streamWinRig) turn() {
	for {
		i := slices.IndexFunc(r.queue, func(q delayed) bool { return q.d == 0 })
		if i < 0 {
			return
		}
		fn := r.queue[i].fn
		r.queue = slices.Delete(r.queue, i, i+1)
		fn()
	}
}

// timeouts runs the queued timeouts.
func (r *streamWinRig) timeouts() {
	q := r.queue
	r.queue = nil
	for _, x := range q {
		if x.d > 0 {
			x.fn()
		}
	}
	r.turn()
}

func (r *streamWinRig) win() *fakeNativeWin { return r.wins[len(r.wins)-1] }

func ready(m *streamWindows) bool {
	m.readyMu.Lock()
	defer m.readyMu.Unlock()
	return m.ready == nil
}

// TestStreamWindowOpensInFullScreen: the window is made on the next turn,
// out of the frame that asked, and goes full screen when the setting says
// so; a stream starting waits for full screen to finish, not before.
func TestStreamWindowOpensInFullScreen(t *testing.T) {
	for _, c := range []struct {
		name       string
		full, sync bool
	}{{"macOS full screen", true, false}, {"Windows full screen", true, true}, {"windowed", false, false}} {
		t.Run(c.name, func(t *testing.T) {
			r := newStreamWinRig(c.full, c.sync)
			r.m.open("bigboi")
			if len(r.wins) != 0 || !r.m.isOpen() || ready(r.m) {
				t.Fatalf("on open: %d windows, open %v, ready %v", len(r.wins), r.m.isOpen(), ready(r.m))
			}
			r.turn()
			w := r.win()
			if w.titles[0] != "bigboi — Planck" || w.full != c.full {
				t.Fatalf("window %q, full screen %v", w.titles, w.full)
			}
			if c.full && !c.sync {
				if ready(r.m) {
					t.Fatal("ready while the full-screen transition runs")
				}
				w.endTransition()
			}
			if !ready(r.m) {
				t.Fatal("not ready once the window reached its size")
			}
		})
	}
}

// TestStreamWindowWaitReady: waitReady returns once the window is in full
// screen, and gives up after its bound.
func TestStreamWindowWaitReady(t *testing.T) {
	r := newStreamWinRig(true, false)
	r.m.open("bigboi")
	r.turn()
	start := time.Now()
	r.m.waitReady(20 * time.Millisecond)
	if time.Since(start) < 20*time.Millisecond {
		t.Fatal("waitReady returned during the transition")
	}
	done := make(chan struct{})
	go func() { r.m.waitReady(5 * time.Second); close(done) }()
	time.Sleep(10 * time.Millisecond)
	r.win().endTransition()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("waitReady did not return once in full screen")
	}
}

// TestStreamWindowCloseLeavesFullScreenFirst: a close leaves full screen,
// waits for the transition and the video's surface, then destroys the
// window, once.
func TestStreamWindowCloseLeavesFullScreenFirst(t *testing.T) {
	r := newStreamWinRig(true, false)
	r.m.open("bigboi")
	r.turn()
	w := r.win()
	w.endTransition()
	r.video = true

	r.m.close()
	if r.m.isOpen() {
		t.Fatal("a closing window reports open")
	}
	r.turn()
	if !slices.Equal(w.fullCalls, []bool{true, false}) || w.destroyed {
		t.Fatalf("full-screen calls %v, destroyed %v", w.fullCalls, w.destroyed)
	}
	r.m.close()
	r.turn()
	w.endTransition()
	r.turn()
	if w.destroyed {
		t.Fatal("destroyed with the video's surface still on it")
	}
	r.video = false
	r.m.videoClosed()
	r.turn()
	if !w.destroyed || r.gones != 1 || r.m.current() != nil {
		t.Fatalf("destroyed %v, gones %d after the video closed", w.destroyed, r.gones)
	}
	if !slices.Equal(w.fullCalls, []bool{true, false}) {
		t.Fatalf("full-screen calls %v", w.fullCalls)
	}
	r.timeouts()
	if r.gones != 1 {
		t.Fatalf("a stale timeout closed again: gones %d", r.gones)
	}
}

// TestStreamWindowCloseDuringEntry: a close while full screen is still
// coming leaves it once it arrived.
func TestStreamWindowCloseDuringEntry(t *testing.T) {
	r := newStreamWinRig(true, false)
	r.m.open("bigboi")
	r.turn()
	w := r.win()
	r.m.close()
	r.turn()
	w.endTransition()
	r.turn()
	if w.destroyed {
		t.Fatal("destroyed before leaving full screen")
	}
	w.endTransition()
	r.turn()
	if !w.destroyed || w.full {
		t.Fatalf("destroyed %v, full screen %v", w.destroyed, w.full)
	}
}

// TestStreamWindowCloseTimeout: a window whose video never lets go closes
// after streamCloseWait.
func TestStreamWindowCloseTimeout(t *testing.T) {
	r := newStreamWinRig(false, false)
	r.m.open("bigboi")
	r.turn()
	r.video = true
	r.m.close()
	r.turn()
	if r.win().destroyed {
		t.Fatal("destroyed with the video still on it")
	}
	r.timeouts()
	if !r.win().destroyed || r.gones != 1 {
		t.Fatal("not destroyed after the timeout")
	}
}

// TestStreamWindowUserClose: closing the window ends the stream and closes
// the window as a close does; while quitting, it closes at once.
func TestStreamWindowUserClose(t *testing.T) {
	r := newStreamWinRig(false, true)
	r.m.open("bigboi")
	r.turn()
	if r.m.closeRequested() {
		t.Fatal("the window closed before the stream ended")
	}
	if r.userEnds != 1 {
		t.Fatalf("stream ends %d", r.userEnds)
	}
	if r.m.closeRequested() || r.userEnds != 1 {
		t.Fatal("a second close request ended the stream again")
	}
	r.turn()
	if !r.win().destroyed {
		t.Fatal("window not closed")
	}

	q := newStreamWinRig(false, true)
	q.m.open("bigboi")
	q.turn()
	q.m.quitting = true
	if !q.m.closeRequested() || q.m.isOpen() {
		t.Fatal("a quit was held up by the stream window")
	}
}

// TestStreamWindowReopen: opening again brings the window to the front
// under the new host's name, and cancels a close under way; a close before
// the window was made makes none.
func TestStreamWindowReopen(t *testing.T) {
	r := newStreamWinRig(false, true)
	r.m.open("bigboi")
	r.turn()
	r.m.open("studio-mac")
	w := r.win()
	if len(r.wins) != 1 || w.focuses != 1 || w.titles[len(w.titles)-1] != "studio-mac — Planck" {
		t.Fatalf("windows %d, focuses %d, titles %q", len(r.wins), w.focuses, w.titles)
	}
	r.video = true
	r.m.close()
	r.m.open("bigboi")
	r.turn()
	r.timeouts()
	if w.destroyed || !r.m.isOpen() {
		t.Fatal("a reopened window closed")
	}

	n := newStreamWinRig(true, true)
	n.m.open("bigboi")
	n.m.close()
	n.turn()
	if len(n.wins) != 0 || n.gones != 1 || !ready(n.m) {
		t.Fatalf("windows %d, gones %d after a close before the window was made", len(n.wins), n.gones)
	}

	e := newStreamWinRig(true, true)
	e.m.ensure()
	if len(e.wins) != 1 || !e.win().full {
		t.Fatal("ensure did not make the window at once")
	}
}
