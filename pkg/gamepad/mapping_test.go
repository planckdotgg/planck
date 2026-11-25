package gamepad

import (
	"reflect"
	"strings"
	"testing"
)

// Lines as SDL's gamecontrollerdb ships them.
const (
	xinputLine = "78696e70757401000000000000000000,XInput Controller,a:b0,b:b1,back:b6,dpdown:h0.4,dpleft:h0.8,dpright:h0.2,dpup:h0.1,guide:b10,leftshoulder:b4,leftstick:b8,lefttrigger:a4,leftx:a0,lefty:a1,rightshoulder:b5,rightstick:b9,righttrigger:a5,rightx:a2,righty:a3,start:b7,x:b2,y:b3,platform:Windows,"
	ps4MacLine = "030000004c050000c405000000010000,PS4 Controller,a:b1,b:b2,back:b8,dpdown:h0.4,dpleft:h0.8,dpright:h0.2,dpup:h0.1,guide:b12,leftshoulder:b4,leftstick:b10,lefttrigger:a3,leftx:a0,lefty:a1,rightshoulder:b5,rightstick:b11,righttrigger:a4,rightx:a2,righty:a5,start:b9,touchpad:b13,x:b0,y:b3,platform:Mac OS X,"
	halfAxes   = "03000000de2800000112000001000000,Steam Controller,a:b0,b:b1,lefttrigger:+a2,leftx:a0~,righttrigger:-a5~,platform:Linux,"
)

func TestSDLMappingRoundTrip(t *testing.T) {
	for _, line := range []string{xinputLine, ps4MacLine, halfAxes} {
		m, err := ParseSDLMapping(line)
		if err != nil {
			t.Fatalf("parse %q: %v", line, err)
		}
		if got := m.FormatSDLMapping(); got != line {
			t.Errorf("format:\n got %s\nwant %s", got, line)
		}
		again, err := ParseSDLMapping(m.FormatSDLMapping())
		if err != nil || !reflect.DeepEqual(again, m) {
			t.Errorf("reparse = %+v, %v; want %+v", again, err, m)
		}
	}
}

func TestParseSDLMappingFields(t *testing.T) {
	m, err := ParseSDLMapping(ps4MacLine)
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "PS4 Controller" || m.GUID != HIDGUID(ConnectionUSB, 0x054C, 0x05C4, 0x0100) {
		t.Errorf("GUID, name = %s, %s", m.GUID, m.Name)
	}
	if b := m.Bindings[InputDPadLeft]; b != (Binding{Kind: SourceHat, Index: 0, HatMask: HatLeft}) {
		t.Errorf("dpleft = %+v", b)
	}
	if b := m.Bindings[InputRightY]; b != (Binding{Kind: SourceAxis, Index: 5}) {
		t.Errorf("righty = %+v", b)
	}
	if want := []string{"touchpad:b13", "platform:Mac OS X"}; !reflect.DeepEqual(m.Extra, want) {
		t.Errorf("extra = %q, want %q", m.Extra, want)
	}
	h, _ := ParseSDLMapping(halfAxes)
	if b := h.Bindings[InputRightTrigger]; b != (Binding{Kind: SourceAxis, Index: 5, Range: RangeNegative, Invert: true}) {
		t.Errorf("righttrigger = %+v", b)
	}
}

func TestParseSDLMappingErrors(t *testing.T) {
	for _, s := range []string{
		"",
		"xyz,Pad,a:b0",
		"030000004c050000c405000000010000,Pad,a:b0,a:b1",
		"030000004c050000c405000000010000,Pad,a:q0",
		"030000004c050000c405000000010000,Pad,a:h0",
		"030000004c050000c405000000010000,Pad,a:+b0",
		"030000004c050000c405000000010000,Pad,a",
	} {
		if _, err := ParseSDLMapping(s); err == nil {
			t.Errorf("ParseSDLMapping(%q) succeeded", s)
		}
	}
}

func TestGUIDs(t *testing.T) {
	if g := XInputGUID(1); g != strings.SplitN(xinputLine, ",", 2)[0] {
		t.Errorf("XInputGUID = %s", g)
	}
	if g := HIDGUID(ConnectionBluetooth, 0x054C, 0x05C4, 0x0100); g != "050000004c050000c405000000010000" {
		t.Errorf("HIDGUID bluetooth = %s", g)
	}
}

func xinputRaw(buttons []int, hat uint8, axes [6]int32) *RawState {
	raw := &RawState{Buttons: make([]bool, 11), Hats: []uint8{hat}}
	for _, b := range buttons {
		raw.Buttons[b] = true
	}
	for i, v := range axes {
		a := Axis{Value: v, Min: -32768, Max: 32767}
		if i >= 4 {
			a.Min, a.Max = 0, 255
		}
		raw.Axes = append(raw.Axes, a)
	}
	return raw
}

func TestDefaultMappingApply(t *testing.T) {
	m := DefaultMapping(XInputGUID(1), "pad")
	// lefty -32768 is SDL's up: the wire's +32767.
	raw := xinputRaw([]int{0, 3, 10}, HatUp|HatLeft, [6]int32{32767, -32768, -32768, 32767, 255, 0})
	got := m.Apply(raw)
	want := State{Buttons: ButtonA | ButtonY | ButtonGuide | ButtonDPadUp | ButtonDPadLeft,
		LX: 32767, LY: 32767, RX: -32768, RY: -32768, LT: 255, RT: 0}
	if got != want {
		t.Errorf("Apply = %+v, want %+v", got, want)
	}
	centred := m.Apply(xinputRaw(nil, 0, [6]int32{0, -1, 0, -1, 128, 0}))
	if centred.LX != 0 || centred.LY != 0 || centred.Buttons != 0 || centred.LT != 128 {
		t.Errorf("centred = %+v", centred)
	}
	if empty := m.Apply(&RawState{}); empty != (State{}) {
		t.Errorf("no inputs = %+v, want released and centred", empty)
	}
}

func TestMapUnmapReset(t *testing.T) {
	m := DefaultMapping(XInputGUID(1), "pad")
	raw := xinputRaw([]int{1}, 0, [6]int32{})
	m.Map(InputA, Binding{Kind: SourceButton, Index: 1})
	m.Unmap(InputB)
	if s := m.Apply(raw); s.Buttons != ButtonA {
		t.Errorf("remapped buttons = %#x, want A", s.Buttons)
	}
	m.Map(InputRightTrigger, Binding{Kind: SourceButton, Index: 1})
	if s := m.Apply(raw); s.RT != 255 {
		t.Errorf("trigger from button = %d", s.RT)
	}
	m.Extra = []string{"platform:Windows"}
	m.Reset()
	if m.Bindings != defaultBindings || m.Extra != nil {
		t.Errorf("Reset left %+v", m)
	}
	if s := m.Apply(raw); s.Buttons != ButtonB {
		t.Errorf("reset buttons = %#x, want B", s.Buttons)
	}

	b := hidDefaultBindings(nil)
	h := Mapping{Bindings: b, defaults: &b}
	h.Map(InputLeftX, Binding{Kind: SourceAxis, Index: 7})
	h.Reset()
	if h.Bindings != b {
		t.Error("Reset did not restore the controller's own defaults")
	}
}

func TestApplyHalfAxesAndInvert(t *testing.T) {
	m, err := ParseSDLMapping(halfAxes)
	if err != nil {
		t.Fatal(err)
	}
	raw := &RawState{Axes: make([]Axis, 6)}
	for i := range raw.Axes {
		raw.Axes[i] = Axis{Min: -32768, Max: 32767}
	}
	raw.Axes[0].Value = 32767  // leftx inverted: full left
	raw.Axes[2].Value = 32767  // +a2: positive half fully in
	raw.Axes[5].Value = -32768 // -a5~ inverted: reads the positive half, 0
	s := m.Apply(raw)
	if s.LX != -32768 || s.LT != 255 || s.RT != 0 {
		t.Errorf("Apply = %+v", s)
	}
}

func TestBindingStrings(t *testing.T) {
	for _, s := range []string{"b0", "a12", "+a2", "-a3", "a4~", "+a5~", "h1.8"} {
		b, err := parseBinding(s)
		if err != nil || b.String() != s {
			t.Errorf("parseBinding(%q) = %v, %v", s, b, err)
		}
	}
}
