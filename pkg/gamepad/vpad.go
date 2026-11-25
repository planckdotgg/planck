package gamepad

import (
	"errors"
	"fmt"
	"slices"
	"sync"
)

// PadType is the controller a host's virtual pads appear as to games.
type PadType uint8

const (
	PadXbox360 PadType = iota
	PadDS4
	PadDualSense
)

var padTypeNames = [...]string{PadXbox360: "xbox360", PadDS4: "ds4", PadDualSense: "dualsense"}

// String is the name config files use.
func (t PadType) String() string {
	if int(t) < len(padTypeNames) {
		return padTypeNames[t]
	}
	return fmt.Sprintf("padtype(%d)", t)
}

func ParsePadType(s string) (PadType, error) {
	for i, n := range padTypeNames {
		if n == s {
			return PadType(i), nil
		}
	}
	return 0, fmt.Errorf("gamepad: unknown virtual pad type %q (want one of %v)", s, padTypeNames)
}

// MaxVirtualPads is how many virtual pads a host plugs in at once, the four
// slots of XInput.
const MaxVirtualPads = 4

// PadDriverStatus is what a host can emulate.
type PadDriverStatus struct {
	Installed      bool
	DriverName     string
	SupportedTypes []PadType
	InstallHint    string // set when Installed is false
}

// Effective is the type a request for t plugs in: t when the driver has
// it, otherwise the closest type it has (a DualSense is emulated as a
// DualShock 4).
func (s PadDriverStatus) Effective(t PadType) PadType {
	if slices.Contains(s.SupportedTypes, t) {
		return t
	}
	if t == PadDualSense && slices.Contains(s.SupportedTypes, PadDS4) {
		return PadDS4
	}
	return PadXbox360
}

var (
	ErrNoFreeSlot = errors.New("gamepad: every virtual pad slot is taken")
	ErrClosed     = errors.New("gamepad: virtual pads closed")
)

// RumbleFunc receives a game's rumble for the client pad feeding a virtual
// pad, motor strengths 0..255.
type RumbleFunc func(pad uint8, large, small uint8)

// padTarget is one plugged-in virtual controller.
type padTarget interface {
	submit(s State) error
	unplug() error
}

// padBus plugs virtual controllers in; a game's rumble on a target reports
// through its onRumble.
type padBus interface {
	plug(t PadType, onRumble func(large, small uint8)) (padTarget, error)
	close() error
}

type padSlot struct {
	pad    int // client pad index, -1 when free
	plug   uint64
	target padTarget
}

// VirtualPads feeds client controllers to virtual ones on the host. Each
// client pad index takes the lowest free slot the first time it sends a
// state, up to MaxVirtualPads. Safe from any goroutine.
type VirtualPads struct {
	mu       sync.Mutex
	bus      padBus
	typ      PadType
	slots    [MaxVirtualPads]padSlot
	onRumble RumbleFunc
	plugs    uint64
	closed   bool
}

func newVirtualPads(bus padBus, t PadType) *VirtualPads {
	v := &VirtualPads{bus: bus, typ: t}
	for i := range v.slots {
		v.slots[i].pad = -1
	}
	return v
}

// SetType changes the type of pads plugged in from now on; ResetSlots
// replugs the ones already in.
func (v *VirtualPads) SetType(t PadType) {
	v.mu.Lock()
	v.typ = t
	v.mu.Unlock()
}

func (v *VirtualPads) SetRumbleHandler(f RumbleFunc) {
	v.mu.Lock()
	v.onRumble = f
	v.mu.Unlock()
}

// rumble forwards a game's rumble on the target of plug to the client pad
// that feeds it now.
func (v *VirtualPads) rumble(plug uint64, large, small uint8) {
	v.mu.Lock()
	f, pad := v.onRumble, -1
	for _, s := range v.slots {
		if s.plug == plug && s.target != nil {
			pad = s.pad
		}
	}
	v.mu.Unlock()
	if f != nil && pad >= 0 {
		f(uint8(pad), large, small)
	}
}

// Submit sends client pad's state to its virtual pad, plugging one in when
// the pad has none.
func (v *VirtualPads) Submit(pad uint8, s State) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return ErrClosed
	}
	free := -1
	for i := range v.slots {
		if v.slots[i].pad == int(pad) {
			return v.slots[i].target.submit(s)
		}
		if free < 0 && v.slots[i].pad < 0 {
			free = i
		}
	}
	if free < 0 {
		return ErrNoFreeSlot
	}
	v.plugs++
	id := v.plugs
	t, err := v.bus.plug(v.typ, func(large, small uint8) { v.rumble(id, large, small) })
	if err != nil {
		return err
	}
	v.slots[free] = padSlot{pad: int(pad), plug: id, target: t}
	return t.submit(s)
}

// Slots are the client pad index in each slot, -1 for a free one.
func (v *VirtualPads) Slots() [MaxVirtualPads]int {
	v.mu.Lock()
	defer v.mu.Unlock()
	var out [MaxVirtualPads]int
	for i, s := range v.slots {
		out[i] = s.pad
	}
	return out
}

// ResetSlots unplugs every virtual pad; each client pad takes a slot again
// in the order it next sends a state.
func (v *VirtualPads) ResetSlots() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.unplugAll()
}

func (v *VirtualPads) unplugAll() error {
	var errs []error
	for i := range v.slots {
		if t := v.slots[i].target; t != nil {
			errs = append(errs, t.unplug())
		}
		v.slots[i] = padSlot{pad: -1}
	}
	return errors.Join(errs...)
}

// ReorderSlot swaps the client pads feeding slots from and to, so that a
// game's player 1 becomes whichever pad was in slot to. The virtual pads
// stay plugged in.
func (v *VirtualPads) ReorderSlot(from, to int) error {
	if from < 0 || from >= MaxVirtualPads || to < 0 || to >= MaxVirtualPads {
		return fmt.Errorf("gamepad: slots %d and %d: want 0 to %d", from, to, MaxVirtualPads-1)
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	a, b := &v.slots[from], &v.slots[to]
	if a.target == nil || b.target == nil {
		*a, *b = *b, *a
		return nil
	}
	a.pad, b.pad = b.pad, a.pad
	return nil
}

// Close unplugs every virtual pad.
func (v *VirtualPads) Close() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return nil
	}
	v.closed = true
	return errors.Join(v.unplugAll(), v.bus.close())
}

// ds4Report is the ViGEmBus DS4_REPORT for s.
type ds4Report struct {
	ThumbLX, ThumbLY, ThumbRX, ThumbRY uint8
	Buttons                            uint16
	Special                            uint8
	TriggerL, TriggerR                 uint8
}

const (
	ds4Square        = 1 << 4
	ds4Cross         = 1 << 5
	ds4Circle        = 1 << 6
	ds4Triangle      = 1 << 7
	ds4ShoulderLeft  = 1 << 8
	ds4ShoulderRight = 1 << 9
	ds4TriggerLeft   = 1 << 10
	ds4TriggerRight  = 1 << 11
	ds4Share         = 1 << 12
	ds4Options       = 1 << 13
	ds4ThumbLeft     = 1 << 14
	ds4ThumbRight    = 1 << 15
	ds4SpecialPS     = 1 << 0
	ds4HatNone       = 8
)

var ds4Buttons = [...]struct {
	from uint32
	to   uint16
}{
	{ButtonA, ds4Cross}, {ButtonB, ds4Circle}, {ButtonX, ds4Square}, {ButtonY, ds4Triangle},
	{ButtonLeftShoulder, ds4ShoulderLeft}, {ButtonRightShoulder, ds4ShoulderRight},
	{ButtonBack, ds4Share}, {ButtonStart, ds4Options},
	{ButtonLeftThumb, ds4ThumbLeft}, {ButtonRightThumb, ds4ThumbRight},
}

// ds4Hat is the DS4 hat value, 0 north clockwise to 7 north-west, for the
// D-pad bits; opposite directions held together cancel.
func ds4Hat(buttons uint32) uint16 {
	dx, dy := 0, 0
	if buttons&ButtonDPadRight != 0 {
		dx++
	}
	if buttons&ButtonDPadLeft != 0 {
		dx--
	}
	if buttons&ButtonDPadUp != 0 {
		dy++
	}
	if buttons&ButtonDPadDown != 0 {
		dy--
	}
	hats := [3][3]uint16{ // [dy+1][dx+1]
		{5, 4, 3},
		{6, ds4HatNone, 2},
		{7, 0, 1},
	}
	return hats[dy+1][dx+1]
}

func ds4Axis(v int16) uint8 { return uint8((int32(v) + 32768) >> 8) }

func toDS4(s State) ds4Report {
	r := ds4Report{
		ThumbLX: ds4Axis(s.LX), ThumbLY: 255 - ds4Axis(s.LY),
		ThumbRX: ds4Axis(s.RX), ThumbRY: 255 - ds4Axis(s.RY),
		Buttons:  ds4Hat(s.Buttons),
		TriggerL: s.LT, TriggerR: s.RT,
	}
	for _, b := range ds4Buttons {
		if s.Buttons&b.from != 0 {
			r.Buttons |= b.to
		}
	}
	if s.LT > 0 {
		r.Buttons |= ds4TriggerLeft
	}
	if s.RT > 0 {
		r.Buttons |= ds4TriggerRight
	}
	if s.Buttons&ButtonGuide != 0 {
		r.Special |= ds4SpecialPS
	}
	return r
}
