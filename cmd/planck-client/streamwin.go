package main

import (
	"sync"
	"time"
)

const (
	// streamCloseWait bounds how long a closing stream window waits to
	// leave full screen and for the video to let it go.
	streamCloseWait = 3 * time.Second
	// streamReadyWait bounds how long a stream starting waits for its
	// window to reach its first size, full screen included.
	streamReadyWait = 2 * time.Second
)

// nativeStreamWin is the stream's window as streamWindows drives it, on
// the main thread: a *mygo.Window.
type nativeStreamWin interface {
	IsFullScreen() bool
	SetFullScreen(on bool)
	SetTitle(title string)
	Focus()
	Destroy()
}

type streamWinState int

const (
	streamWinNone streamWinState = iota
	// streamWinOpening: asked for, made on the main loop's next turn, out
	// of the frame that asked.
	streamWinOpening
	streamWinOpen
	// streamWinClosing: leaving full screen, then waiting for the video's
	// surface to let the window go.
	streamWinClosing
)

// streamWindows opens and closes the window a stream shows in, apart from
// the launcher's. Its methods run on the main thread, except waitReady.
type streamWindows struct {
	// create makes the window, titled title, with its events wired here.
	create func(title string) nativeStreamWin
	// fullscreen reports whether a new window opens in full screen.
	fullscreen func() bool
	// videoLive reports whether a video surface is still on the window.
	videoLive func() bool
	// userClosed tells the app the user closed the window.
	userClosed func()
	// gone hears that the window is closed.
	gone func()
	// later runs fn on the main thread after d; 0 is the next turn.
	later func(d time.Duration, fn func())

	state streamWinState
	win   nativeStreamWin
	host  string
	// leaving is set from asking a closing window to leave full screen
	// until a transition ends.
	leaving bool
	// gen counts windows and closes, so that a stale timeout does nothing.
	gen int
	// quitting lets every close through while the app quits.
	quitting bool

	// ready is open while a window is on its way to its first size.
	readyMu sync.Mutex
	ready   chan struct{}
}

func streamTitle(host string) string {
	if host == "" {
		return "Planck"
	}
	return host + " — Planck"
}

// open opens the window for a stream from host, or brings the one open,
// or closing, back to the front under host's name.
func (m *streamWindows) open(host string) {
	if host != "" {
		m.host = host
	}
	switch m.state {
	case streamWinNone:
		m.state = streamWinOpening
		m.gen++
		m.setReady(false)
		m.later(0, m.realize)
	case streamWinClosing:
		m.state, m.leaving = streamWinOpen, false
		m.gen++
		fallthrough
	case streamWinOpen:
		m.win.SetTitle(streamTitle(m.host))
		m.win.Focus()
	}
}

// ensure has the window made by now, for a stream's video.
func (m *streamWindows) ensure() {
	if m.state == streamWinNone || m.state == streamWinClosing {
		m.open("")
	}
	if m.state == streamWinOpening {
		m.realize()
	}
}

func (m *streamWindows) realize() {
	if m.state != streamWinOpening {
		return
	}
	m.win = m.create(streamTitle(m.host))
	m.state = streamWinOpen
	if m.fullscreen() {
		m.win.SetFullScreen(true)
		return
	}
	m.setReady(true)
}

func (m *streamWindows) isOpen() bool {
	return m.state == streamWinOpening || m.state == streamWinOpen
}

// current is the window, nil before it is made and once it is gone.
func (m *streamWindows) current() nativeStreamWin { return m.win }

// close closes the window once it left full screen and the video let it
// go, or after streamCloseWait.
func (m *streamWindows) close() {
	switch m.state {
	case streamWinOpening:
		m.state = streamWinNone
		m.setReady(true)
		m.gone()
	case streamWinOpen:
		m.state = streamWinClosing
		m.gen++
		gen := m.gen
		m.later(streamCloseWait, func() {
			if m.gen == gen && m.state == streamWinClosing {
				m.destroy()
			}
		})
		m.later(0, m.step)
	}
}

func (m *streamWindows) step() {
	if m.state != streamWinClosing || m.leaving {
		return
	}
	if m.win.IsFullScreen() {
		m.leaving = true
		m.win.SetFullScreen(false)
		return
	}
	if m.videoLive() {
		return
	}
	m.destroy()
}

func (m *streamWindows) destroy() {
	w := m.win
	m.win, m.state, m.leaving = nil, streamWinNone, false
	m.gen++
	w.Destroy()
	m.setReady(true)
	m.gone()
}

// fullScreenChanged hears the window finish entering or leaving full
// screen. One entered while closing, as a close came during the
// transition, leaves it again.
func (m *streamWindows) fullScreenChanged(full bool) {
	if full {
		m.setReady(true)
	}
	if m.state == streamWinClosing {
		m.leaving = false
		m.later(0, m.step)
	}
}

// videoClosed hears the video's surface leave the window.
func (m *streamWindows) videoClosed() {
	if m.state == streamWinClosing {
		m.later(0, m.step)
	}
}

// closeRequested hears the user close the window, and reports whether it
// may close now: only while the app quits. Otherwise the stream ends and
// the window closes as close does.
func (m *streamWindows) closeRequested() bool {
	if m.quitting {
		m.win, m.state = nil, streamWinNone
		m.setReady(true)
		return true
	}
	if m.state == streamWinOpen {
		m.userClosed()
		m.close()
	}
	return false
}

// setReady opens or closes the gate waitReady waits at.
func (m *streamWindows) setReady(ready bool) {
	m.readyMu.Lock()
	defer m.readyMu.Unlock()
	switch {
	case ready && m.ready != nil:
		close(m.ready)
		m.ready = nil
	case !ready && m.ready == nil:
		m.ready = make(chan struct{})
	}
}

// waitReady waits, up to d, for a window on its way to reach its first
// size: made, and in full screen if it opens there. Not on the main
// thread.
func (m *streamWindows) waitReady(d time.Duration) {
	m.readyMu.Lock()
	ch := m.ready
	m.readyMu.Unlock()
	if ch == nil {
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ch:
	case <-t.C:
	}
}

// streamWindowHook is streamWindows as the UI drives it.
type streamWindowHook struct{ m *streamWindows }

func (h streamWindowHook) OpenStreamWindow(host string) { h.m.open(host) }
func (h streamWindowHook) CloseStreamWindow()           { h.m.close() }
func (h streamWindowHook) StreamWindowOpen() bool       { return h.m.isOpen() }
