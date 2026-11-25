package gamepad

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
)

// Input is one input of the standard controller a mapping fills.
type Input uint8

const (
	InputA Input = iota
	InputB
	InputX
	InputY
	InputBack
	InputGuide
	InputStart
	InputLeftStick
	InputRightStick
	InputLeftShoulder
	InputRightShoulder
	InputDPadUp
	InputDPadDown
	InputDPadLeft
	InputDPadRight
	InputLeftX
	InputLeftY
	InputRightX
	InputRightY
	InputLeftTrigger
	InputRightTrigger
	NumInputs
)

// sdlNames are the field names of an SDL gamecontrollerdb mapping.
var sdlNames = [NumInputs]string{
	"a", "b", "x", "y", "back", "guide", "start", "leftstick", "rightstick",
	"leftshoulder", "rightshoulder", "dpup", "dpdown", "dpleft", "dpright",
	"leftx", "lefty", "rightx", "righty", "lefttrigger", "righttrigger",
}

func (in Input) String() string {
	if in < NumInputs {
		return sdlNames[in]
	}
	return "input(" + strconv.Itoa(int(in)) + ")"
}

var inputButtons = [...]uint32{
	InputA: ButtonA, InputB: ButtonB, InputX: ButtonX, InputY: ButtonY,
	InputBack: ButtonBack, InputGuide: ButtonGuide, InputStart: ButtonStart,
	InputLeftStick: ButtonLeftThumb, InputRightStick: ButtonRightThumb,
	InputLeftShoulder: ButtonLeftShoulder, InputRightShoulder: ButtonRightShoulder,
	InputDPadUp: ButtonDPadUp, InputDPadDown: ButtonDPadDown,
	InputDPadLeft: ButtonDPadLeft, InputDPadRight: ButtonDPadRight,
}

type SourceKind uint8

const (
	SourceNone SourceKind = iota
	SourceButton
	SourceAxis
	SourceHat
)

// AxisRange is the part of a source axis a binding reads: +a2 and -a2 in a
// mapping string are its halves.
type AxisRange uint8

const (
	RangeFull AxisRange = iota
	RangePositive
	RangeNegative
)

// Binding is where one Input comes from on the raw controller.
type Binding struct {
	Kind    SourceKind
	Index   int
	HatMask uint8 // SourceHat: the direction bits
	Range   AxisRange
	Invert  bool // ~ in a mapping string
}

func (b Binding) String() string {
	switch b.Kind {
	case SourceButton:
		return "b" + strconv.Itoa(b.Index)
	case SourceHat:
		return "h" + strconv.Itoa(b.Index) + "." + strconv.Itoa(int(b.HatMask))
	case SourceAxis:
		s := "a" + strconv.Itoa(b.Index)
		switch b.Range {
		case RangePositive:
			s = "+" + s
		case RangeNegative:
			s = "-" + s
		}
		if b.Invert {
			s += "~"
		}
		return s
	}
	return ""
}

func parseBinding(s string) (Binding, error) {
	var b Binding
	src := s
	switch {
	case strings.HasPrefix(src, "+"):
		b.Range, src = RangePositive, src[1:]
	case strings.HasPrefix(src, "-"):
		b.Range, src = RangeNegative, src[1:]
	}
	if strings.HasSuffix(src, "~") {
		b.Invert, src = true, src[:len(src)-1]
	}
	if src == "" {
		return b, fmt.Errorf("gamepad: empty binding %q", s)
	}
	kind, rest := src[0], src[1:]
	if kind == 'h' {
		idx, mask, ok := strings.Cut(rest, ".")
		i, err1 := strconv.Atoi(idx)
		m, err2 := strconv.Atoi(mask)
		if !ok || err1 != nil || err2 != nil || i < 0 || m <= 0 || m > 15 || b.Range != RangeFull || b.Invert {
			return b, fmt.Errorf("gamepad: bad hat binding %q", s)
		}
		b.Kind, b.Index, b.HatMask = SourceHat, i, uint8(m)
		return b, nil
	}
	i, err := strconv.Atoi(rest)
	if err != nil || i < 0 {
		return b, fmt.Errorf("gamepad: bad binding %q", s)
	}
	switch kind {
	case 'b':
		if b.Range != RangeFull || b.Invert {
			return b, fmt.Errorf("gamepad: bad button binding %q", s)
		}
		b.Kind = SourceButton
	case 'a':
		b.Kind = SourceAxis
	default:
		return b, fmt.Errorf("gamepad: bad binding %q", s)
	}
	b.Index = i
	return b, nil
}

// Mapping turns one controller's RawState into a State.
type Mapping struct {
	GUID     string
	Name     string
	Bindings [NumInputs]Binding
	// Halves drive one half of a stick output each, [0] the positive and
	// [1] the negative, as +leftx: and -leftx: do in a mapping string; they
	// add to the stick's full binding.
	Halves [NumInputs][2]Binding
	// Extra are fields of an imported mapping string this package does not
	// use, such as platform:, kept so that export gives them back.
	Extra []string
	// defaults are what Reset restores; nil is the XInput layout.
	defaults *[NumInputs]Binding
}

// The XInput layout as SDL indexes it; XInput controllers read raw in this
// order, so it is also the default for controllers with no mapping.
var defaultBindings = func() [NumInputs]Binding {
	btn := func(i int) Binding { return Binding{Kind: SourceButton, Index: i} }
	axis := func(i int) Binding { return Binding{Kind: SourceAxis, Index: i} }
	hat := func(m uint8) Binding { return Binding{Kind: SourceHat, HatMask: m} }
	return [NumInputs]Binding{
		InputA: btn(0), InputB: btn(1), InputX: btn(2), InputY: btn(3),
		InputLeftShoulder: btn(4), InputRightShoulder: btn(5), InputBack: btn(6), InputStart: btn(7),
		InputLeftStick: btn(8), InputRightStick: btn(9), InputGuide: btn(10),
		InputDPadUp: hat(HatUp), InputDPadRight: hat(HatRight), InputDPadDown: hat(HatDown), InputDPadLeft: hat(HatLeft),
		InputLeftX: axis(0), InputLeftY: axis(1), InputRightX: axis(2), InputRightY: axis(3),
		InputLeftTrigger: axis(4), InputRightTrigger: axis(5),
	}
}()

// DefaultMapping is the mapping a controller with no saved one starts with.
func DefaultMapping(guid, name string) Mapping {
	return Mapping{GUID: guid, Name: name, Bindings: defaultBindings}
}

func (m *Mapping) Map(in Input, b Binding) { m.Bindings[in], m.Halves[in] = b, [2]Binding{} }

func (m *Mapping) Unmap(in Input) { m.Map(in, Binding{}) }

func isStick(in Input) bool { return in >= InputLeftX && in <= InputRightY }

// Reset puts every input back on the binding its controller starts with
// and drops imported extras. A parsed mapping resets to the XInput layout.
func (m *Mapping) Reset() {
	m.Bindings, m.Halves, m.Extra = defaultBindings, [NumInputs][2]Binding{}, nil
	if m.defaults != nil {
		m.Bindings = *m.defaults
	}
}

// value reads b on raw as 0..1 for a half axis, a button or a hat, and
// -1..1 for a full axis; ok is false when the source does not exist.
func (b Binding) value(raw *RawState) (v float64, half, ok bool) {
	switch b.Kind {
	case SourceButton:
		if b.Index < len(raw.Buttons) && raw.Buttons[b.Index] {
			return 1, true, true
		}
		return 0, true, b.Index < len(raw.Buttons)
	case SourceHat:
		if b.Index >= len(raw.Hats) {
			return 0, true, false
		}
		if raw.Hats[b.Index]&b.HatMask != 0 {
			return 1, true, true
		}
		return 0, true, true
	case SourceAxis:
		if b.Index >= len(raw.Axes) {
			return 0, false, false
		}
		v = raw.Axes[b.Index].Norm()
		if b.Invert {
			v = -v
		}
		switch b.Range {
		case RangePositive:
			return math.Max(0, v), true, true
		case RangeNegative:
			return math.Max(0, -v), true, true
		}
		return v, false, true
	}
	return 0, false, false
}

// Apply maps raw onto the wire layout. Unmapped and missing inputs read as
// released or centred.
func (m *Mapping) Apply(raw *RawState) State {
	var s State
	for in := InputA; in <= InputDPadRight; in++ {
		v, half, ok := m.Bindings[in].value(raw)
		if !half {
			v = (v + 1) / 2
		}
		if ok && v > 0.5 {
			s.Buttons |= inputButtons[in]
		}
	}
	stick := func(in Input) float64 {
		v, half, _ := m.Bindings[in].value(raw)
		if half {
			v = 2*v - 1
			if m.Bindings[in].Kind != SourceAxis && v < 0 {
				v = 0
			}
		}
		for i, sign := range [2]float64{1, -1} {
			if h := m.Halves[in][i]; h.Kind != SourceNone {
				hv, half, ok := h.value(raw)
				if !ok {
					continue
				}
				if !half {
					hv = (hv + 1) / 2
				}
				v += sign * hv
			}
		}
		return v
	}
	trigger := func(in Input) uint8 {
		v, half, ok := m.Bindings[in].value(raw)
		if !ok {
			return 0
		}
		if !half {
			v = (v + 1) / 2
		}
		return uint8(math.Round(v * 255))
	}
	s.LX, s.LY = toInt16(stick(InputLeftX)), toInt16(-stick(InputLeftY))
	s.RX, s.RY = toInt16(stick(InputRightX)), toInt16(-stick(InputRightY))
	s.LT, s.RT = trigger(InputLeftTrigger), trigger(InputRightTrigger)
	return s
}

func toInt16(v float64) int16 {
	v = math.Max(-1, math.Min(1, v))
	if v < 0 {
		return int16(math.Round(v * 32768))
	}
	return int16(math.Round(v * 32767))
}

// FormatSDLMapping writes m as an SDL gamecontrollerdb line: fields in the
// alphabetical order the database uses, platform last.
func (m *Mapping) FormatSDLMapping() string {
	var fields, platform []string
	for in := range NumInputs {
		if b := m.Bindings[in]; b.Kind != SourceNone {
			fields = append(fields, sdlNames[in]+":"+b.String())
		}
		for i, sign := range [2]string{"+", "-"} {
			if b := m.Halves[in][i]; b.Kind != SourceNone {
				fields = append(fields, sign+sdlNames[in]+":"+b.String())
			}
		}
	}
	for _, e := range m.Extra {
		if strings.HasPrefix(e, "platform:") {
			platform = append(platform, e)
		} else {
			fields = append(fields, e)
		}
	}
	key := func(f string) string { k, _, _ := strings.Cut(f, ":"); return k }
	slices.SortStableFunc(fields, func(a, b string) int { return strings.Compare(key(a), key(b)) })
	var sb strings.Builder
	sb.WriteString(m.GUID + "," + m.Name + ",")
	for _, f := range append(fields, platform...) {
		sb.WriteString(f + ",")
	}
	return sb.String()
}

// ParseSDLMapping reads one SDL gamecontrollerdb line. Inputs it does not
// name stay unmapped.
func ParseSDLMapping(s string) (Mapping, error) {
	fields := strings.Split(strings.TrimSpace(s), ",")
	if len(fields) < 2 {
		return Mapping{}, fmt.Errorf("gamepad: mapping %q: want GUID,name,bindings", s)
	}
	m := Mapping{GUID: strings.ToLower(fields[0]), Name: fields[1]}
	if !ValidGUID(m.GUID) {
		return Mapping{}, fmt.Errorf("gamepad: mapping GUID %q: want 32 hex digits", fields[0])
	}
	var seen [NumInputs][3]bool // full, positive half, negative half
	for _, f := range fields[2:] {
		if f = strings.TrimSpace(f); f == "" {
			continue
		}
		key, val, ok := strings.Cut(f, ":")
		if !ok {
			return Mapping{}, fmt.Errorf("gamepad: mapping field %q: want name:binding", f)
		}
		part, name := 0, key
		if len(key) > 0 && (key[0] == '+' || key[0] == '-') {
			part, name = 1+strings.IndexByte("+-", key[0]), key[1:]
		}
		in := Input(slices.Index(sdlNames[:], name))
		if in >= NumInputs || val == "" || (part > 0 && !isStick(in)) {
			m.Extra = append(m.Extra, f)
			continue
		}
		if seen[in][part] {
			return Mapping{}, fmt.Errorf("gamepad: mapping binds %s twice", key)
		}
		b, err := parseBinding(val)
		if err != nil {
			return Mapping{}, err
		}
		if part == 0 {
			m.Bindings[in] = b
		} else {
			m.Halves[in][part-1] = b
		}
		seen[in][part] = true
	}
	return m, nil
}

func ValidGUID(g string) bool {
	if len(g) != 32 {
		return false
	}
	_, err := hex.DecodeString(g)
	return err == nil
}

// SDL bus types in a joystick GUID.
const (
	busUSB       = 0x03
	busBluetooth = 0x05
)

// HIDGUID is the SDL joystick GUID of a HID controller: bus, CRC (zero),
// then vendor, product and version, little endian with zero padding.
func HIDGUID(c Connection, vendor, product, version uint16) string {
	var g [16]byte
	bus := uint16(busUSB)
	if c == ConnectionBluetooth {
		bus = busBluetooth
	}
	binary.LittleEndian.PutUint16(g[0:], bus)
	binary.LittleEndian.PutUint16(g[4:], vendor)
	binary.LittleEndian.PutUint16(g[8:], product)
	binary.LittleEndian.PutUint16(g[12:], version)
	return hex.EncodeToString(g[:])
}

// XInputGUID is SDL's GUID for an XInput controller of a subtype
// (XINPUT_DEVSUBTYPE_*: 1 is a gamepad).
func XInputGUID(subtype uint8) string {
	var g [16]byte
	copy(g[:], "xinput")
	g[6] = subtype
	return hex.EncodeToString(g[:])
}
