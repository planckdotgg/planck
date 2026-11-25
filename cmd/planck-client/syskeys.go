package main

import (
	"errors"
	"sync"

	mui "github.com/egoist/mygo/ui"
	"github.com/planckdotgg/planck/pkg/input"
)

// keyTap is the OS's keyboard hook that immersive mode captures the
// system's keys with: a CGEventTap on macOS, WH_KEYBOARD_LL on Windows.
// start installs it on a thread of its own, which calls on with each key
// event until stop; on reports whether the OS must not see the event.
// stop returns once on is no longer called.
type keyTap interface {
	start(on func(tapEvent) bool) error
	stop()
}

// tapEvent is a key event as the hook saw it.
type tapEvent struct {
	// usage is the key's HID usage, 0 for a key the host has none for.
	usage uint16
	down  bool
	// chord is the Ctrl, Alt and Shift the OS holds, which tell the
	// overlay's keys, even ones pressed before the capture began.
	chord mui.Modifiers
}

// errTapDenied is a hook the OS would not install for lack of a
// permission; its text is what the overlay shows.
type errTapDenied struct{ hint string }

func (e errTapDenied) Error() string { return e.hint }

// systemKeys is immersive mode. While it is on, the stream's window has the
// focus, its menu is closed and a stream runs, a keyboard hook takes every
// key and sends it to the host, Cmd-Tab, Alt-Tab and the Windows key
// included, and the stream view sends none of its own. Ctrl, Shift and Alt
// reach the window as well, and the overlay's keys reach it alone, so that
// its shortcuts still work. A key goes up wherever it went down; whatever
// the hook sent down goes up as the capture stops.
type systemKeys struct {
	tap  keyTap
	send func(input.Event)
	logf func(string, ...any)
	// local are the chords that stay on this device.
	local *localKeys
	// changed asks for a frame, from any goroutine.
	changed func()

	// applyMu keeps one start or stop of the hook at a time.
	applyMu sync.Mutex

	mu                                   sync.Mutex
	enabled, active, menuOpen, streaming bool
	// failed holds the capture off after the hook would not start, until
	// one of the above changes; problem says why, until it starts.
	failed  bool
	problem string
	running bool
	held    input.Held
}

func newSystemKeys(tap keyTap, send func(input.Event), logf func(string, ...any)) *systemKeys {
	return &systemKeys{tap: tap, send: send, logf: logf}
}

// setEnabled turns immersive mode on or off. Safe from any goroutine.
func (s *systemKeys) setEnabled(on bool) {
	if !on {
		s.mu.Lock()
		s.problem = ""
		s.mu.Unlock()
	}
	s.set(&s.enabled, on)
}

// setMenuOpen holds the capture off while the stream's menu is open.
func (s *systemKeys) setMenuOpen(open bool) { s.set(&s.menuOpen, open) }

// streamEnded stops the capture as the stream's view goes.
func (s *systemKeys) streamEnded() { s.set(&s.streaming, false) }

// frame takes whether the stream's window has the focus, once a frame of
// the stream view, and reports whether the hook has the keyboard.
func (s *systemKeys) frame(active bool) bool {
	s.set(&s.streaming, true)
	s.set(&s.active, active)
	return s.capturing()
}

func (s *systemKeys) capturing() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// SystemKeysProblem is why the capture could not start, "" if it could or
// was not tried.
func (s *systemKeys) SystemKeysProblem() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.problem
}

func (s *systemKeys) set(field *bool, v bool) {
	s.mu.Lock()
	changed := *field != v
	*field = v
	if changed {
		s.failed = false
	}
	s.mu.Unlock()
	if changed {
		s.apply()
	}
}

func (s *systemKeys) wanted() bool {
	return s.tap != nil && s.enabled && s.active && !s.menuOpen && s.streaming && !s.failed
}

// apply starts or stops the hook to match what is wanted, which may change
// while it does.
func (s *systemKeys) apply() {
	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	for {
		s.mu.Lock()
		want := s.wanted()
		if want == s.running {
			s.mu.Unlock()
			return
		}
		if !want {
			s.running = false
			for _, ev := range s.held.Release() {
				ev.Key.SystemKeyCapture = true
				s.send(ev)
			}
			s.mu.Unlock()
			s.tap.stop()
			continue
		}
		s.mu.Unlock()
		err := s.tap.start(s.event)
		s.mu.Lock()
		if err != nil {
			s.failed = true
			var denied errTapDenied
			if errors.As(err, &denied) {
				s.problem = denied.hint
			} else {
				s.problem = "System keys stay on this device: " + err.Error()
			}
			s.mu.Unlock()
			if s.logf != nil {
				s.logf("planck: immersive mode cannot capture system keys: %v", err)
			}
			if s.changed != nil {
				s.changed()
			}
			return
		}
		s.running, s.problem = true, ""
		s.mu.Unlock()
	}
}

// event is the hook's: it sends a captured key to the host and reports
// whether the OS must not see it.
func (s *systemKeys) event(e tapEvent) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running || e.usage == 0 || e.usage > 0xff {
		return false
	}
	was := s.held.KeyDown(e.usage)
	// The OS's chord lacks Cmd and the Windows key; the hook sent them.
	if e.down && !was && s.local.has(e.chord|guiModifier(s.held.Modifiers()), e.usage) {
		return false
	}
	if e.down == was {
		// A repeat of a key sent down, or the release of one pressed
		// before the capture, which the window has.
		return e.down && takenFromOS(e.usage)
	}
	mods := s.held.Modifiers()
	if bit := modifierBit(e.usage); bit != 0 {
		if e.down {
			mods |= bit
		} else {
			mods &^= bit
		}
	}
	ev := input.Event{Type: input.TypeKey, Key: input.KeyEvent{Usage: e.usage, Pressed: e.down, Modifiers: mods, SystemKeyCapture: true}}
	s.held.Note(ev)
	s.send(ev)
	return takenFromOS(e.usage)
}

// takenFromOS is whether a captured key is kept from the OS: every key but
// Ctrl, Shift and Alt, by which the window tells its shortcuts.
func takenFromOS(usage uint16) bool {
	switch usage {
	case input.KeyLeftControl, input.KeyRightControl, input.KeyLeftShift, input.KeyRightShift,
		input.KeyLeftAlt, input.KeyRightAlt:
		return false
	}
	return true
}

func modifierBit(usage uint16) uint8 {
	if usage >= input.KeyLeftControl && usage <= input.KeyRightGUI {
		return 1 << (usage - input.KeyLeftControl)
	}
	return 0
}
