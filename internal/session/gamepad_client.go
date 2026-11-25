package session

import (
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/planckdotgg/planck/pkg/gamepad"
	"github.com/planckdotgg/planck/pkg/input"
	"github.com/planckdotgg/planck/pkg/wire"
)

// GamepadSource reads a device's controllers; *gamepad.Capture is one.
type GamepadSource interface {
	Controllers() []gamepad.ControllerInfo
	Read(id int, raw *gamepad.RawState) error
	Rumble(id int, large, small uint8) error
	DefaultMapping(id int) gamepad.Mapping
}

// GamepadProfile is the user's processing of their controllers.
type GamepadProfile struct {
	Calibration gamepad.Calibration
	// Mappings are the saved mappings by lower-case SDL GUID; other
	// controllers use their source's default.
	Mappings map[string]gamepad.Mapping
}

// PadSnapshot is one controller as the pump last read it.
type PadSnapshot struct {
	Info gamepad.ControllerInfo
	// Raw is its inputs before mapping, and State after mapping and
	// calibration: what a stream sends.
	Raw   gamepad.RawState
	State gamepad.State
	// Mapping is the one in use; Custom says it is a saved one.
	Mapping gamepad.Mapping
	Custom  bool
	// Learning is the input Learn waits on for this controller.
	Learning   gamepad.Input
	IsLearning bool
}

const (
	// padPollEvery is how often attached controllers are read: 500 Hz,
	// at or above what most controllers report at.
	padPollEvery = 2 * time.Millisecond
	// padScanEvery is how often the controller list is read again; it is
	// the slow call (a HID device copy, or XInput on empty slots).
	padScanEvery = 500 * time.Millisecond
	// padRepeats resend a state that stopped changing, padRepeatEvery
	// apart, so a lost datagram does not leave a button held or a stick
	// pushed on the host.
	padRepeats     = 3
	padRepeatEvery = 16 * time.Millisecond
	// padWatchFor keeps the pump reading for Settings this long after its
	// last Watch; padNotifyEvery bounds how often a watcher hears of
	// changes.
	padWatchFor    = time.Second
	padNotifyEvery = 33 * time.Millisecond
	// padLearnFor is how long Learn waits for a control to move.
	padLearnFor = 10 * time.Second
)

type padLearn struct {
	id     int
	target gamepad.Input
	rest   *gamepad.RawState
	until  time.Time
	done   func(gamepad.Binding)
}

// padOut is what a stream last sent of one pad index.
type padOut struct {
	sent    bool
	state   gamepad.State
	repeats int
	at      time.Time
}

// GamepadPump reads this device's controllers in one goroutine, which
// runs only while a stream is attached or Settings watches. It maps and
// calibrates each controller, sends a stream the states that change, plays
// the host's rumble, and keeps a snapshot for Settings to read each frame
// without touching the controllers itself. Safe from any goroutine.
type GamepadPump struct {
	open     func() (GamepadSource, error)
	logf     func(string, ...any)
	onChange func()

	mu      sync.Mutex
	src     GamepadSource
	opened  bool
	profile GamepadProfile
	running bool
	closed  bool
	// send is the attached stream's, and attach counts attachments so an
	// old stream's Detach leaves a newer one alone.
	send       func(input.Event) error
	attach     uint64
	watchUntil time.Time
	snap       []PadSnapshot
	learn      *padLearn
	// rumble is the host's last rumble per pad index, and rumbleSeq its
	// sequence, to drop one that arrives late.
	rumble    [input.MaxGamepads]*wire.Rumble
	rumbleSeq [input.MaxGamepads]uint32
}

// NewGamepadPump reads the controllers open gives it, opened on first use
// in the pump's goroutine. onChange hears the snapshot change while
// Settings watches.
func NewGamepadPump(open func() (GamepadSource, error), onChange func(), logf func(string, ...any)) *GamepadPump {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &GamepadPump{open: open, onChange: onChange, logf: logf, profile: GamepadProfile{Calibration: gamepad.DefaultCalibration()}}
}

// OpenGamepads is NewGamepadPump's open for this device's controllers.
func OpenGamepads() (GamepadSource, error) { return gamepad.Open() }

func (p *GamepadPump) SetProfile(pr GamepadProfile) {
	p.mu.Lock()
	p.profile = pr
	p.mu.Unlock()
}

// Attach sends each controller's state to send, a stream's SendInput,
// from now on, as the pad index of its slot. It returns the id Detach
// takes.
func (p *GamepadPump) Attach(send func(input.Event) error) uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.attach++
	p.send = send
	p.rumble, p.rumbleSeq = [input.MaxGamepads]*wire.Rumble{}, [input.MaxGamepads]uint32{}
	p.startLocked()
	return p.attach
}

// Detach stops sending for attachment id, after letting go of every input
// it sent held and stopping the rumble it played.
func (p *GamepadPump) Detach(id uint64) {
	p.mu.Lock()
	if p.attach != id || p.send == nil {
		p.mu.Unlock()
		return
	}
	p.send = nil
	p.rumble = [input.MaxGamepads]*wire.Rumble{}
	p.mu.Unlock()
	// The loop sends the neutral states and stops the motors as it sees
	// the stream gone.
}

// Watch keeps the pump reading for a second more and returns its last
// snapshot, for a Settings page drawn each frame.
func (p *GamepadPump) Watch() []PadSnapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.watchUntil = time.Now().Add(padWatchFor)
	p.startLocked()
	return slices.Clone(p.snap)
}

// Rumble plays a host's rumble on the controller in pad's slot, scaled by
// the calibration; a rumble older than the last one heard is dropped.
func (p *GamepadPump) Rumble(r wire.Rumble) {
	if int(r.Pad) >= input.MaxGamepads {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.send == nil {
		return
	}
	if last := p.rumble[r.Pad]; last != nil && int32(r.Seq-p.rumbleSeq[r.Pad]) < 0 {
		return
	}
	p.rumble[r.Pad], p.rumbleSeq[r.Pad] = &r, r.Seq
}

// Learn waits up to ten seconds for a control of controller id to move,
// then gives done the binding for target. Another Learn replaces it.
func (p *GamepadPump) Learn(id int, target gamepad.Input, done func(gamepad.Binding)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.learn = &padLearn{id: id, target: target, until: time.Now().Add(padLearnFor), done: done}
	p.watchUntil = time.Now().Add(padWatchFor)
	p.startLocked()
}

// CancelLearn stops waiting for a control to move.
func (p *GamepadPump) CancelLearn() {
	p.mu.Lock()
	p.learn = nil
	p.mu.Unlock()
}

// TestRumble shakes controller id for d at the calibration's strength.
func (p *GamepadPump) TestRumble(id int, d time.Duration) error {
	p.mu.Lock()
	src, cal := p.src, p.profile.Calibration
	p.mu.Unlock()
	if src == nil {
		return gamepad.ErrNotConnected
	}
	large, small := cal.ScaleRumble(0xFFFF, 0xFFFF)
	if err := src.Rumble(id, large, small); err != nil {
		return err
	}
	time.AfterFunc(d, func() { _ = src.Rumble(id, 0, 0) })
	return nil
}

// Close stops the pump and closes its source.
func (p *GamepadPump) Close() {
	p.mu.Lock()
	p.closed, p.send, p.watchUntil = true, nil, time.Time{}
	src, running := p.src, p.running
	p.mu.Unlock()
	if !running {
		closeSource(src)
	}
}

func closeSource(src GamepadSource) {
	if c, ok := src.(io.Closer); ok {
		c.Close()
	}
}

func (p *GamepadPump) startLocked() {
	if p.running || p.closed {
		return
	}
	p.running = true
	go p.run()
}

// activeLocked is whether the loop has work.
func (p *GamepadPump) activeLocked(now time.Time) bool {
	return !p.closed && (p.send != nil || now.Before(p.watchUntil) || p.learn != nil)
}

func (p *GamepadPump) source() GamepadSource {
	p.mu.Lock()
	src, opened := p.src, p.opened
	p.opened = true
	p.mu.Unlock()
	if opened {
		return src
	}
	src, err := p.open()
	if err != nil {
		p.logf("planck: gamepads: %v", err)
		src = nil
	}
	p.mu.Lock()
	p.src = src
	p.mu.Unlock()
	return src
}

func (p *GamepadPump) run() {
	src := p.source()
	var (
		pads    []gamepad.ControllerInfo
		raws    []gamepad.RawState
		scanned time.Time
		out     [input.MaxGamepads]padOut
		// played is the host rumble each slot's motors play, and motors
		// the speeds sent for it, so a calibration change reaches running
		// motors.
		played   [input.MaxGamepads]*wire.Rumble
		motors   [input.MaxGamepads][2]uint8
		sending  func(input.Event) error
		attached uint64
		notified time.Time
	)
	stopAll := func() {
		for i := range out {
			if out[i].sent && sending != nil {
				_ = sending(gamepadEvent(i, gamepad.State{}))
			}
			out[i] = padOut{}
			if played[i] != nil {
				if pad := padInSlot(pads, i); pad != nil {
					_ = src.Rumble(pad.ID, 0, 0)
				}
				played[i] = nil
			}
			motors[i] = [2]uint8{}
		}
	}
	for {
		now := time.Now()
		p.mu.Lock()
		if !p.activeLocked(now) {
			p.running = false
			closed := p.closed
			p.mu.Unlock()
			if src != nil {
				stopAll()
				if closed {
					closeSource(src)
				}
			}
			return
		}
		send, profile, learn, watching := p.send, p.profile, p.learn, now.Before(p.watchUntil)
		rumble, attach := p.rumble, p.attach
		p.mu.Unlock()
		if src == nil {
			p.idle(learn)
			time.Sleep(padScanEvery)
			continue
		}
		// A replacing stream has heard nothing yet: the old one lets go,
		// and the new one gets every state at once, not on its next change.
		if sending != nil && (send == nil || attach != attached) {
			stopAll()
		}
		sending, attached = send, attach

		if now.Sub(scanned) >= padScanEvery {
			pads, scanned = src.Controllers(), now
			raws = slices.Grow(raws[:0], len(pads))[:len(pads)]
		}
		var snap []PadSnapshot
		live := [input.MaxGamepads]bool{}
		for i, info := range pads {
			raw := &raws[i]
			if err := src.Read(info.ID, raw); err != nil {
				continue
			}
			m, custom := mappingFor(src, info, profile)
			st := profile.Calibration.Apply(m.Apply(raw))
			slot := info.Slot
			if slot >= 0 && slot < input.MaxGamepads {
				live[slot] = true
				if send != nil {
					o := &out[slot]
					switch {
					case !o.sent || o.state != st:
						*o = padOut{sent: true, state: st, repeats: padRepeats, at: now}
						_ = send(gamepadEvent(slot, st))
					case o.repeats > 0 && now.Sub(o.at) >= padRepeatEvery:
						o.repeats--
						o.at = now
						_ = send(gamepadEvent(slot, st))
					}
					r := rumble[slot]
					var m [2]uint8
					if r != nil {
						m[0], m[1] = profile.Calibration.ScaleRumble(r.Low, r.High)
					}
					if r != played[slot] || m != motors[slot] {
						if err := src.Rumble(info.ID, m[0], m[1]); err == nil {
							played[slot], motors[slot] = r, m
						}
					}
				}
			}
			if learn != nil && learn.id == info.ID {
				p.step(learn, raw, now)
			}
			if watching {
				s := PadSnapshot{Info: info, Raw: cloneRaw(raw), State: st, Mapping: m, Custom: custom}
				if learn != nil && learn.id == info.ID {
					s.Learning, s.IsLearning = learn.target, true
				}
				snap = append(snap, s)
			}
		}
		for slot := range out {
			if out[slot].sent && !live[slot] {
				if send != nil {
					_ = send(gamepadEvent(slot, gamepad.State{}))
				}
				out[slot] = padOut{}
			}
		}
		if learn != nil && now.After(learn.until) {
			p.endLearn(learn)
		}
		if watching {
			p.mu.Lock()
			same := snapshotsEqual(p.snap, snap)
			if !same {
				p.snap = snap
			}
			p.mu.Unlock()
			if !same && p.onChange != nil && now.Sub(notified) >= padNotifyEvery {
				notified = now
				p.onChange()
			}
		}
		wait := padPollEvery
		if len(pads) == 0 {
			wait = padScanEvery
		}
		time.Sleep(wait)
	}
}

// idle runs a loop turn with no source: a Learn can only time out.
func (p *GamepadPump) idle(learn *padLearn) {
	if learn != nil && time.Now().After(learn.until) {
		p.endLearn(learn)
	}
}

func (p *GamepadPump) endLearn(l *padLearn) {
	p.mu.Lock()
	if p.learn == l {
		p.learn = nil
	}
	p.mu.Unlock()
}

// step takes the controller's state at a Learn's start as rest, and gives
// done the first control that moved from it.
func (p *GamepadPump) step(l *padLearn, raw *gamepad.RawState, now time.Time) {
	if l.rest == nil {
		r := cloneRaw(raw)
		l.rest = &r
		return
	}
	b, ok := gamepad.Learn(l.rest, raw, l.target)
	if !ok {
		return
	}
	p.mu.Lock()
	mine := p.learn == l
	if mine {
		p.learn = nil
	}
	p.mu.Unlock()
	if mine && l.done != nil {
		go l.done(b)
	}
}

// mappingFor is the controller's saved mapping, or its default.
func mappingFor(src GamepadSource, info gamepad.ControllerInfo, pr GamepadProfile) (gamepad.Mapping, bool) {
	if m, ok := pr.Mappings[strings.ToLower(info.GUID)]; ok {
		return m, true
	}
	return src.DefaultMapping(info.ID), false
}

func padInSlot(pads []gamepad.ControllerInfo, slot int) *gamepad.ControllerInfo {
	for i := range pads {
		if pads[i].Slot == slot {
			return &pads[i]
		}
	}
	return nil
}

func gamepadEvent(slot int, s gamepad.State) input.Event {
	return input.Event{Type: input.TypeGamepad, Gamepad: input.GamepadState{Index: uint8(slot), Buttons: s.Buttons,
		LeftTrigger: s.LT, RightTrigger: s.RT, LeftX: s.LX, LeftY: s.LY, RightX: s.RX, RightY: s.RY}}
}

func cloneRaw(r *gamepad.RawState) gamepad.RawState {
	return gamepad.RawState{Buttons: slices.Clone(r.Buttons), Axes: slices.Clone(r.Axes), Hats: slices.Clone(r.Hats)}
}

func snapshotsEqual(a, b []PadSnapshot) bool {
	return slices.EqualFunc(a, b, func(x, y PadSnapshot) bool {
		return x.Info == y.Info && x.State == y.State && x.Custom == y.Custom && x.IsLearning == y.IsLearning &&
			x.Learning == y.Learning && slices.Equal(x.Raw.Buttons, y.Raw.Buttons) && slices.Equal(x.Raw.Axes, y.Raw.Axes) &&
			slices.Equal(x.Raw.Hats, y.Raw.Hats) && x.Mapping.Bindings == y.Mapping.Bindings && x.Mapping.Halves == y.Mapping.Halves
	})
}
