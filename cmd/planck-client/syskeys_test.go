package main

import (
	"errors"
	"slices"
	"testing"

	mui "github.com/egoist/mygo/ui"
	"github.com/planckdotgg/planck/pkg/input"
)

// fakeTap stands in for the OS hook: press delivers a key event as the
// hook would, while it is installed.
type fakeTap struct {
	on            func(tapEvent) bool
	starts, stops int
	err           error
}

func (f *fakeTap) start(on func(tapEvent) bool) error {
	f.starts++
	if f.err != nil {
		return f.err
	}
	f.on = on
	return nil
}

func (f *fakeTap) stop() {
	f.stops++
	f.on = nil
}

func (f *fakeTap) installed() bool { return f.on != nil }

// press reports whether the OS would have seen the event: true with no
// hook installed.
func (f *fakeTap) press(usage uint16, down bool, chord mui.Modifiers) bool {
	if f.on == nil {
		return true
	}
	return !f.on(tapEvent{usage: usage, down: down, chord: chord})
}

type sysRig struct {
	tap  *fakeTap
	sys  *systemKeys
	sent []input.Event
}

func newSysRig() *sysRig {
	r := &sysRig{tap: &fakeTap{}}
	r.sys = newSystemKeys(r.tap, func(ev input.Event) { r.sent = append(r.sent, ev) }, nil)
	return r
}

func (r *sysRig) take() []input.Event {
	out := r.sent
	r.sent = nil
	return out
}

func sysKey(usage uint16, down bool, mods uint8) input.Event {
	return input.Event{Type: input.TypeKey, Key: input.KeyEvent{Usage: usage, Pressed: down, Modifiers: mods, SystemKeyCapture: true}}
}

// TestSystemKeysCaptureNeedsEverything: the hook is in only while
// immersive mode is on, the stream's window has the focus, its menu is
// closed and a stream runs.
func TestSystemKeysCaptureNeedsEverything(t *testing.T) {
	r := newSysRig()
	if r.sys.frame(true) || r.tap.installed() {
		t.Fatal("captured with immersive mode off")
	}
	r.sys.setEnabled(true)
	if !r.tap.installed() || !r.sys.frame(true) {
		t.Fatal("no capture with immersive mode on and the window focused")
	}
	for _, c := range []struct {
		name       string
		off, again func()
	}{
		{"focus away", func() { r.sys.frame(false) }, func() { r.sys.frame(true) }},
		{"menu open", func() { r.sys.setMenuOpen(true) }, func() { r.sys.setMenuOpen(false) }},
		{"immersive off", func() { r.sys.setEnabled(false) }, func() { r.sys.setEnabled(true) }},
		{"stream ended", r.sys.streamEnded, func() { r.sys.frame(true) }},
	} {
		c.off()
		if r.tap.installed() || r.sys.capturing() {
			t.Fatalf("%s: the hook stayed in", c.name)
		}
		c.again()
		if !r.tap.installed() || !r.sys.capturing() {
			t.Fatalf("after %s: the hook did not come back", c.name)
		}
	}
	if r.tap.starts != 5 || r.tap.stops != 4 {
		t.Fatalf("%d starts and %d stops, want 5 and 4", r.tap.starts, r.tap.stops)
	}
}

// TestSystemKeysCaptureCmdTab: Cmd-Tab and the Windows key go to the host
// and not to the OS; Ctrl, Shift and Alt go to both, so the window still
// reads its shortcuts; a key repeat is not sent again.
func TestSystemKeysCaptureCmdTab(t *testing.T) {
	r := newSysRig()
	r.sys.setEnabled(true)
	r.sys.frame(true)
	steps := []struct {
		usage   uint16
		down    bool
		chord   mui.Modifiers
		toOS    bool
		toHost  bool
		hostMod uint8
	}{
		{input.KeyLeftGUI, true, 0, false, true, input.ModLeftGUI},
		{input.KeyTab, true, 0, false, true, input.ModLeftGUI},
		{input.KeyTab, true, 0, false, false, 0},
		{input.KeyTab, false, 0, false, true, input.ModLeftGUI},
		{input.KeyLeftGUI, false, 0, false, true, 0},
		{input.KeyLeftAlt, true, 0, true, true, input.ModLeftAlt},
		{input.KeyEscape, true, mui.Alt, false, true, input.ModLeftAlt},
		{input.KeyEscape, false, mui.Alt, false, true, input.ModLeftAlt},
		{input.KeyLeftAlt, false, mui.Alt, true, true, 0},
	}
	for i, st := range steps {
		if toOS := r.tap.press(st.usage, st.down, st.chord); toOS != st.toOS {
			t.Fatalf("step %d (%#x down=%v): the OS saw it %v, want %v", i, st.usage, st.down, toOS, st.toOS)
		}
		got := r.take()
		var want []input.Event
		if st.toHost {
			want = []input.Event{sysKey(st.usage, st.down, st.hostMod)}
		}
		if !slices.Equal(got, want) {
			t.Fatalf("step %d: sent %+v, want %+v", i, got, want)
		}
	}
}

// TestSystemKeysOverlayChordsStayLocal: the overlay's keys reach the
// window and never the host, even with Ctrl and Alt pressed before the
// capture began, which the hook did not see go down.
func TestSystemKeysOverlayChordsStayLocal(t *testing.T) {
	r := newSysRig()
	r.sys.setEnabled(true)
	r.sys.frame(true)
	chord := mui.Ctrl | mui.Alt
	for _, k := range []uint16{input.KeyO, input.KeyP, input.KeyH, input.KeyM, input.KeyU, input.KeyF, input.KeyI, input.KeyG} {
		if !r.tap.press(k, true, chord) || !r.tap.press(k, false, chord) {
			t.Fatalf("Ctrl+Alt+%#x was kept from the window", k)
		}
	}
	if !r.tap.press(input.KeyD, true, chord|mui.Shift) || !r.tap.press(input.KeyD, false, chord|mui.Shift) {
		t.Fatal("Ctrl+Alt+Shift+D was kept from the window")
	}
	if got := r.take(); len(got) != 0 {
		t.Fatalf("overlay keys went to the host: %+v", got)
	}
	if r.tap.press(input.KeyD, true, chord) {
		t.Fatal("Ctrl+Alt+D, no overlay key, reached the OS")
	}
	r.tap.press(input.KeyD, false, chord)
	if got := r.take(); len(got) != 2 {
		t.Fatalf("Ctrl+Alt+D sent %+v, want its press and release", got)
	}
}

// TestSystemKeysReleaseOnSuspend: whatever the hook sent down goes up as
// the capture stops, for each reason it stops, keys before modifiers.
func TestSystemKeysReleaseOnSuspend(t *testing.T) {
	for _, c := range []struct {
		name string
		stop func(*systemKeys)
	}{
		{"focus away", func(s *systemKeys) { s.frame(false) }},
		{"menu open", func(s *systemKeys) { s.setMenuOpen(true) }},
		{"immersive off", func(s *systemKeys) { s.setEnabled(false) }},
		{"disconnect", (*systemKeys).streamEnded},
	} {
		r := newSysRig()
		r.sys.setEnabled(true)
		r.sys.frame(true)
		r.tap.press(input.KeyLeftAlt, true, 0)
		r.tap.press(input.KeyTab, true, mui.Alt)
		r.take()
		c.stop(r.sys)
		want := []input.Event{sysKey(input.KeyTab, false, input.ModLeftAlt), sysKey(input.KeyLeftAlt, false, 0)}
		if got := r.take(); !slices.Equal(got, want) {
			t.Fatalf("%s: sent %+v, want %+v", c.name, got, want)
		}
		if !r.tap.press(input.KeyTab, false, mui.Alt) {
			t.Fatalf("%s: a release after the capture was kept from the OS", c.name)
		}
		if got := r.take(); len(got) != 0 {
			t.Fatalf("%s: a release after the capture went to the host: %+v", c.name, got)
		}
	}
}

// TestSystemKeysNoDoubleSend: the stream view's keys and the hook's never
// both reach the host. As the hook takes over, the keys the view sent go
// up once and the view sends no more; as it lets go, the view carries on.
func TestSystemKeysNoDoubleSend(t *testing.T) {
	r := newSysRig()
	f := &inputForwarder{send: func(ev input.Event) { r.sent = append(r.sent, ev) }, sys: r.sys}
	f.keysFrame([]mui.Key{mui.KeyW}, mui.Shift, true)
	if got := r.take(); len(got) != 2 {
		t.Fatalf("before immersive mode the view sent %+v, want Shift and W", got)
	}
	r.sys.setEnabled(true)
	f.keysFrame([]mui.Key{mui.KeyW}, mui.Shift, true)
	want := []input.Event{
		{Type: input.TypeKey, Key: input.KeyEvent{Usage: input.KeyW, Modifiers: input.ModLeftShift}},
		{Type: input.TypeKey, Key: input.KeyEvent{Usage: input.KeyLeftShift}},
	}
	if got := r.take(); !slices.Equal(got, want) {
		t.Fatalf("hand-over sent %+v, want %+v", got, want)
	}
	r.tap.press(input.KeyA, true, 0)
	f.keysFrame([]mui.Key{mui.KeyA}, 0, true)
	r.tap.press(input.KeyA, true, 0)
	r.tap.press(input.KeyA, false, 0)
	f.keysFrame(nil, 0, true)
	want = []input.Event{sysKey(input.KeyA, true, 0), sysKey(input.KeyA, false, 0)}
	if got := r.take(); !slices.Equal(got, want) {
		t.Fatalf("A while captured sent %+v, want %+v once each", got, want)
	}
	r.sys.setEnabled(false)
	f.keysFrame([]mui.Key{mui.KeyB}, 0, true)
	want = []input.Event{{Type: input.TypeKey, Key: input.KeyEvent{Usage: input.KeyB, Pressed: true}}}
	if got := r.take(); !slices.Equal(got, want) {
		t.Fatalf("after immersive mode the view sent %+v, want %+v", got, want)
	}
}

// TestSystemKeysHookDenied: a hook the OS refuses, as macOS does without
// the Accessibility permission, leaves the keyboard to the stream view,
// says why, and is tried again only when something changes, not each
// frame.
func TestSystemKeysHookDenied(t *testing.T) {
	r := newSysRig()
	r.tap.err = errTapDenied{hint: "Allow Planck in Accessibility"}
	var asked int
	r.sys.changed = func() { asked++ }
	f := &inputForwarder{send: func(ev input.Event) { r.sent = append(r.sent, ev) }, sys: r.sys}
	r.sys.setEnabled(true)
	for range 3 {
		f.keysFrame([]mui.Key{mui.KeyA}, 0, true)
	}
	if r.tap.starts != 1 {
		t.Fatalf("%d starts over three frames, want 1", r.tap.starts)
	}
	if p := r.sys.SystemKeysProblem(); p != "Allow Planck in Accessibility" || asked != 1 {
		t.Fatalf("problem %q after %d redraws", p, asked)
	}
	if got := r.take(); len(got) != 1 || got[0].Key.Usage != input.KeyA || got[0].Key.SystemKeyCapture {
		t.Fatalf("the view sent %+v, want A as usual", got)
	}
	f.keysFrame(nil, 0, false)
	f.keysFrame(nil, 0, true)
	if r.tap.starts != 2 {
		t.Fatalf("the focus coming back tried %d times in all, want 2", r.tap.starts)
	}
	r.sys.setEnabled(false)
	if p := r.sys.SystemKeysProblem(); p != "" {
		t.Fatalf("problem %q with immersive mode off", p)
	}
	r.tap.err = errors.New("tap failed")
	r.sys.setEnabled(true)
	if p := r.sys.SystemKeysProblem(); p == "" {
		t.Fatal("no problem for a hook that failed")
	}
}
