package session

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/planckdotgg/planck/pkg/audio"
	"github.com/planckdotgg/planck/pkg/config"
	"github.com/planckdotgg/planck/pkg/encode"
	"github.com/planckdotgg/planck/pkg/gamepad"
	"github.com/planckdotgg/planck/pkg/input"
	"github.com/planckdotgg/planck/pkg/wire"
)

// fakePads is a GamepadSource of XInput-layout controllers whose raw
// state a test sets.
type fakePads struct {
	mu      sync.Mutex
	pads    []gamepad.ControllerInfo
	raw     map[int]gamepad.RawState
	rumbles [][3]int
	closed  bool
}

const xinputGUID = "78696e70757401000000000000000000"

func newFakePads(n int) *fakePads {
	f := &fakePads{raw: map[int]gamepad.RawState{}}
	for i := range n {
		f.pads = append(f.pads, gamepad.ControllerInfo{ID: 10 + i, Slot: i, Name: "Test Pad", GUID: xinputGUID, BatteryPct: -1, Connected: true})
		f.raw[10+i] = restRaw()
	}
	return f
}

func restRaw() gamepad.RawState {
	stick := gamepad.Axis{Min: -32768, Max: 32767}
	trig := gamepad.Axis{Min: 0, Max: 255}
	return gamepad.RawState{Buttons: make([]bool, 11), Axes: []gamepad.Axis{stick, stick, stick, stick, trig, trig}, Hats: []uint8{0}}
}

func (f *fakePads) set(id int, edit func(r *gamepad.RawState)) {
	f.mu.Lock()
	r := f.raw[id]
	r = cloneRaw(&r)
	edit(&r)
	f.raw[id] = r
	f.mu.Unlock()
}

func (f *fakePads) unplug(id int) {
	f.mu.Lock()
	f.pads = slices.DeleteFunc(f.pads, func(p gamepad.ControllerInfo) bool { return p.ID == id })
	delete(f.raw, id)
	f.mu.Unlock()
}

func (f *fakePads) Controllers() []gamepad.ControllerInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.pads)
}

func (f *fakePads) Read(id int, raw *gamepad.RawState) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.raw[id]
	if !ok {
		return gamepad.ErrNotConnected
	}
	raw.Buttons, raw.Axes, raw.Hats = append(raw.Buttons[:0], r.Buttons...), append(raw.Axes[:0], r.Axes...), append(raw.Hats[:0], r.Hats...)
	return nil
}

func (f *fakePads) Rumble(id int, large, small uint8) error {
	f.mu.Lock()
	f.rumbles = append(f.rumbles, [3]int{id, int(large), int(small)})
	f.mu.Unlock()
	return nil
}

func (f *fakePads) lastRumble() [3]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.rumbles) == 0 {
		return [3]int{-1, -1, -1}
	}
	return f.rumbles[len(f.rumbles)-1]
}

func (f *fakePads) DefaultMapping(id int) gamepad.Mapping {
	return gamepad.DefaultMapping(xinputGUID, "Test Pad")
}

func (f *fakePads) Close() error {
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
	return nil
}

// padInjector records what a host injects and stands in for ViGEmBus: a
// gamepad event plugs its pad into the lowest free slot.
type padInjector struct {
	recordInjector
	mu       sync.Mutex
	typ      gamepad.PadType
	slots    [gamepad.MaxVirtualPads]int
	resets   int
	onRumble gamepad.RumbleFunc
}

func newPadInjector() *padInjector {
	return &padInjector{slots: [gamepad.MaxVirtualPads]int{-1, -1, -1, -1}}
}

func (p *padInjector) Inject(ev input.Event) error {
	if ev.Type == input.TypeGamepad {
		p.mu.Lock()
		if !slices.Contains(p.slots[:], int(ev.Gamepad.Index)) {
			if i := slices.Index(p.slots[:], -1); i >= 0 {
				p.slots[i] = int(ev.Gamepad.Index)
			}
		}
		p.mu.Unlock()
	}
	return p.recordInjector.Inject(ev)
}

func (p *padInjector) SetGamepadType(t gamepad.PadType)      { p.mu.Lock(); p.typ = t; p.mu.Unlock() }
func (p *padInjector) VirtualPads() (VirtualPadSlots, error) { return p, nil }
func (p *padInjector) Slots() [gamepad.MaxVirtualPads]int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.slots
}
func (p *padInjector) ResetSlots() error {
	p.mu.Lock()
	p.slots, p.resets = [gamepad.MaxVirtualPads]int{-1, -1, -1, -1}, p.resets+1
	p.mu.Unlock()
	return nil
}
func (p *padInjector) ReorderSlot(from, to int) error {
	p.mu.Lock()
	p.slots[from], p.slots[to] = p.slots[to], p.slots[from]
	p.mu.Unlock()
	return nil
}
func (p *padInjector) SetRumbleHandler(f gamepad.RumbleFunc) {
	p.mu.Lock()
	p.onRumble = f
	p.mu.Unlock()
}
func (p *padInjector) rumble(pad, large, small uint8) {
	p.mu.Lock()
	f := p.onRumble
	p.mu.Unlock()
	if f != nil {
		f(pad, large, small)
	}
}
func (p *padInjector) state() (gamepad.PadType, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.typ, p.resets
}

func (p *padInjector) lastPad(index uint8) (input.GamepadState, bool) {
	evs := p.events()
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].Type == input.TypeGamepad && evs[i].Gamepad.Index == index {
			return evs[i].Gamepad, true
		}
	}
	return input.GamepadState{}, false
}

func startPadHost(t *testing.T, gp config.GamepadConfig, inj *padInjector) *Host {
	t.Helper()
	dir := t.TempDir()
	var forced, kbps atomic.Int64
	if err := config.SaveGamepadConfig(dir, gp); err != nil {
		t.Fatal(err)
	}
	h, err := NewHost(HostConfig{
		ConfigDir: dir, Name: "pad-host", ListenAddr: "127.0.0.1:0", DiscoveryAddr: "off", FPS: 60, NoTailscale: true,
		OpenVideo: func(p VideoParams) (VideoPipe, error) {
			return &fakePipe{fps: p.FPS, forced: &forced, kbps: &kbps}, nil
		},
		OpenAudio:   func() (audio.Capturer, error) { return &fakeCapturer{}, nil },
		InjectInput: true, Injector: inj, Logf: t.Logf,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Stop)
	return h
}

func connectPadClient(t *testing.T, h *Host, pump *GamepadPump) *Client {
	t.Helper()
	c, err := NewClient(ClientConfig{
		ConfigDir: t.TempDir(), Name: "pad-client", PIN: h.PIN(), NoAudio: true,
		Quality:  Quality{Codec: encode.HEVC, FPS: 60, BitrateKbps: 10_000},
		OpenSink: NullSinkOpener(&NullSink{}), Tailscale: noTailscale{}, Logf: t.Logf, Gamepads: pump,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Disconnect)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.Connect(ctx, Target{Addr: h.Addr().String()}); err != nil {
		t.Fatal(err)
	}
	return c
}

// TestGamepadLoopback: a client controller, through the user's mapping
// and deadzone, reaches the host's virtual controller of the configured
// type; the game's rumble comes back scaled by the rumble strength; slots
// reorder and reset on the host, and unplug when the client leaves.
func TestGamepadLoopback(t *testing.T) {
	gp := config.DefaultGamepadConfig()
	gp.VirtualType = config.VirtualPadDualSense
	inj := newPadInjector()
	h := startPadHost(t, gp, inj)
	if typ, _ := inj.state(); typ != gamepad.PadDualSense {
		t.Fatalf("virtual type %v, want dualsense from gamepad.json", typ)
	}

	src := newFakePads(2)
	pump := NewGamepadPump(func() (GamepadSource, error) { return src, nil }, nil, t.Logf)
	t.Cleanup(pump.Close)
	swapped := gamepad.DefaultMapping(xinputGUID, "Test Pad")
	swapped.Bindings[gamepad.InputA], swapped.Bindings[gamepad.InputB] = swapped.Bindings[gamepad.InputB], swapped.Bindings[gamepad.InputA]
	cal := gamepad.DefaultCalibration()
	cal.LeftDeadzone, cal.RumbleStrength = 20, 50
	pump.SetProfile(GamepadProfile{Calibration: cal, Mappings: map[string]gamepad.Mapping{xinputGUID: swapped}})
	c := connectPadClient(t, h, pump)

	src.set(10, func(r *gamepad.RawState) {
		r.Buttons[0] = true      // A, which the mapping sends as B
		r.Axes[0].Value = 5000   // inside the 20 % deadzone
		r.Axes[5].Value = 255    // right trigger
		r.Axes[3].Value = -32768 // right stick fully up: SDL's Y grows down
	})
	waitFor(t, "pad 0's state at the host", func() bool {
		g, ok := inj.lastPad(0)
		return ok && g.Buttons == gamepad.ButtonB && g.RightTrigger == 255 && g.LeftX == 0 && g.RightY == 32767
	})
	src.set(11, func(r *gamepad.RawState) { r.Buttons[3] = true })
	waitFor(t, "pad 1 in slot 2", func() bool { return h.GamepadSlots() == [4]int{0, 1, -1, -1} })

	waitFor(t, "the host's stream", func() bool { return h.ConnectedClient() != "" })
	inj.rumble(1, 200, 100)
	waitFor(t, "the rumble on the client's second controller", func() bool { return src.lastRumble() == [3]int{11, 100, 50} })

	if err := h.ReorderGamepadSlot(0, 1); err != nil {
		t.Fatal(err)
	}
	if got := h.GamepadSlots(); got != [4]int{1, 0, -1, -1} {
		t.Errorf("slots after reorder = %v", got)
	}
	if err := h.ResetGamepadSlots(); err != nil {
		t.Fatal(err)
	}
	if _, resets := inj.state(); resets != 1 || h.GamepadSlots() != [4]int{-1, -1, -1, -1} {
		t.Errorf("after reset: %d resets, slots %v", resets, h.GamepadSlots())
	}

	c.Disconnect()
	waitFor(t, "the client's motors to stop", func() bool { return src.lastRumble() == [3]int{11, 0, 0} })
	waitFor(t, "the host to unplug the client's controllers", func() bool { _, r := inj.state(); return r >= 2 })
}

// TestGamepadsRefused: with gamepads off the host injects the client's
// keys but none of its controller input, and turning them off unplugs
// what was plugged in.
func TestGamepadsRefused(t *testing.T) {
	inj := newPadInjector()
	h := startPadHost(t, config.DefaultGamepadConfig(), inj)
	src := newFakePads(1)
	pump := NewGamepadPump(func() (GamepadSource, error) { return src, nil }, nil, t.Logf)
	t.Cleanup(pump.Close)
	c := connectPadClient(t, h, pump)
	src.set(10, func(r *gamepad.RawState) { r.Buttons[0] = true })
	waitFor(t, "the pad plugged in", func() bool { return h.GamepadSlots()[0] == 0 })

	if err := h.SetGamepadSettings(GamepadHostSettings{Allow: false, Type: gamepad.PadXbox360}); err != nil {
		t.Fatal(err)
	}
	if _, resets := inj.state(); resets != 1 {
		t.Fatalf("turning gamepads off made %d resets, want 1", resets)
	}
	before := len(inj.events())
	src.set(10, func(r *gamepad.RawState) { r.Buttons[1] = true })
	if err := c.SendInput(input.Event{Type: input.TypeKey, Key: input.KeyEvent{Usage: input.KeyTab, Pressed: true}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the key at the host", func() bool {
		return slices.ContainsFunc(inj.events()[before:], func(ev input.Event) bool { return ev.Type == input.TypeKey })
	})
	time.Sleep(50 * time.Millisecond)
	for _, ev := range inj.events()[before:] {
		if ev.Type == input.TypeGamepad {
			t.Fatalf("injected %+v with gamepads off", ev.Gamepad)
		}
	}
}

// recordSends is a stream's SendInput that keeps what it was given.
type recordSends struct {
	mu  sync.Mutex
	evs []input.GamepadState
}

func (r *recordSends) send(ev input.Event) error {
	r.mu.Lock()
	r.evs = append(r.evs, ev.Gamepad)
	r.mu.Unlock()
	return nil
}

func (r *recordSends) all() []input.GamepadState {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.evs)
}

// TestGamepadPumpSends: a state that stops changing is sent a few more
// times and then not again, and a controller that goes away is let go of.
func TestGamepadPumpSends(t *testing.T) {
	src := newFakePads(1)
	pump := NewGamepadPump(func() (GamepadSource, error) { return src, nil }, nil, t.Logf)
	t.Cleanup(pump.Close)
	rec := &recordSends{}
	id := pump.Attach(rec.send)
	src.set(10, func(r *gamepad.RawState) { r.Buttons[2] = true })
	waitFor(t, "X pressed", func() bool {
		evs := rec.all()
		return len(evs) > 0 && evs[len(evs)-1].Buttons == gamepad.ButtonX
	})
	time.Sleep(200 * time.Millisecond)
	n := 0
	for _, g := range rec.all() {
		if g.Buttons == gamepad.ButtonX {
			n++
		}
	}
	if n != 1+padRepeats {
		t.Errorf("X sent %d times, want %d", n, 1+padRepeats)
	}

	src.unplug(10)
	waitFor(t, "the gone controller let go of", func() bool {
		evs := rec.all()
		return evs[len(evs)-1] == input.GamepadState{}
	})
	pump.Detach(id)
	pump.Rumble(wire.Rumble{Pad: 0, Low: 0xFFFF})
	if got := src.lastRumble(); got[0] != -1 {
		t.Errorf("a detached pump played rumble: %v", got)
	}
}

// TestGamepadPumpLearn: Learn binds the first control that moves from
// where it rested, and Watch's snapshot shows the wait.
func TestGamepadPumpLearn(t *testing.T) {
	src := newFakePads(1)
	src.set(10, func(r *gamepad.RawState) { r.Buttons[1] = true }) // held from the start: not the answer
	pump := NewGamepadPump(func() (GamepadSource, error) { return src, nil }, nil, t.Logf)
	t.Cleanup(pump.Close)
	got := make(chan gamepad.Binding, 1)
	pump.Learn(10, gamepad.InputStart, func(b gamepad.Binding) { got <- b })
	waitFor(t, "the snapshot to show the wait", func() bool {
		s := pump.Watch()
		return len(s) == 1 && s[0].IsLearning && s[0].Learning == gamepad.InputStart
	})
	src.set(10, func(r *gamepad.RawState) { r.Buttons[5] = true })
	select {
	case b := <-got:
		if b != (gamepad.Binding{Kind: gamepad.SourceButton, Index: 5}) {
			t.Errorf("learned %+v, want b5", b)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Learn heard nothing")
	}
	waitFor(t, "the wait to end", func() bool { s := pump.Watch(); return len(s) == 1 && !s[0].IsLearning })
}

// pausedInjector holds each gamepad event inside Inject until proceed.
type pausedInjector struct {
	*padInjector
	entered, proceed chan struct{}
}

func (p *pausedInjector) Inject(ev input.Event) error {
	close(p.entered)
	<-p.proceed
	return p.padInjector.Inject(ev)
}

// TestGamepadDisableDuringInject: turning gamepads off while the first
// gamepad event is inside Inject leaves no virtual controller plugged in.
func TestGamepadDisableDuringInject(t *testing.T) {
	inj := &pausedInjector{newPadInjector(), make(chan struct{}), make(chan struct{})}
	h := &Host{inject: inj}
	h.pads.set = GamepadHostSettings{Allow: true, Type: gamepad.PadXbox360}
	ev, err := toWire(gamepadEvent(0, gamepad.State{Buttons: gamepad.ButtonA}))
	if err != nil {
		t.Fatal(err)
	}
	injected := make(chan struct{})
	go func() { h.onInput(nil, ev); close(injected) }()
	select {
	case <-inj.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("injection did not start")
	}
	disabled := make(chan error, 1)
	go func() { disabled <- h.SetGamepadSettings(GamepadHostSettings{Allow: false, Type: gamepad.PadXbox360}) }()
	time.Sleep(20 * time.Millisecond)
	close(inj.proceed)
	<-injected
	if err := <-disabled; err != nil {
		t.Fatal(err)
	}
	if got := h.GamepadSlots(); got != [gamepad.MaxVirtualPads]int{-1, -1, -1, -1} {
		t.Fatalf("slots after disabling = %v, want all free", got)
	}
}

// TestGamepadPumpReplacementGetsHeldState: a stream that replaces another
// gets a held button at once, though the old one sent its last repeat.
func TestGamepadPumpReplacementGetsHeldState(t *testing.T) {
	src := newFakePads(1)
	p := NewGamepadPump(func() (GamepadSource, error) { return src, nil }, nil, t.Logf)
	t.Cleanup(p.Close)
	src.set(10, func(r *gamepad.RawState) { r.Buttons[0] = true })
	old, next := &recordSends{}, &recordSends{}
	id := p.Attach(old.send)
	waitFor(t, "the old stream's repeats", func() bool { return len(old.all()) == 1+padRepeats })
	time.Sleep(3 * padRepeatEvery)
	p.Attach(next.send)
	p.Detach(id)
	deadline := time.Now().Add(time.Second)
	for len(next.all()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the replacing stream heard nothing of the held A")
		}
		time.Sleep(time.Millisecond)
	}
	if got := next.all()[0]; got.Buttons&gamepad.ButtonA == 0 {
		t.Fatalf("replacing stream's first state = %+v, want A held", got)
	}
}

// TestGamepadPumpRumbleOffStopsMotors: turning rumble off stops motors a
// host rumble started, without another rumble packet.
func TestGamepadPumpRumbleOffStopsMotors(t *testing.T) {
	src := newFakePads(1)
	p := NewGamepadPump(func() (GamepadSource, error) { return src, nil }, nil, t.Logf)
	t.Cleanup(p.Close)
	p.Attach((&recordSends{}).send)
	p.Rumble(wire.Rumble{Pad: 0, Seq: 1, Low: 65535})
	waitFor(t, "the motor to start", func() bool { return src.lastRumble() == [3]int{10, 255, 0} })
	cal := gamepad.DefaultCalibration()
	cal.RumbleStrength = 50
	p.SetProfile(GamepadProfile{Calibration: cal})
	waitFor(t, "the motor at half strength", func() bool { return src.lastRumble() == [3]int{10, 128, 0} })
	cal.RumbleOff = true
	p.SetProfile(GamepadProfile{Calibration: cal})
	deadline := time.Now().Add(time.Second)
	for src.lastRumble() != [3]int{10, 0, 0} {
		if time.Now().After(deadline) {
			t.Fatalf("motor after rumble off = %v, want stopped", src.lastRumble())
		}
		time.Sleep(time.Millisecond)
	}
}
