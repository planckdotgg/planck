package gamepad

import (
	"slices"
	"strings"
	"testing"

	"github.com/planckdotgg/planck/internal/linux/evdev"
)

// xpadCaps is an Xbox 360 controller as xpad reports it.
func xpadCaps() evdevCaps {
	stick := evdevAxis{Min: -32768, Max: 32767}
	trig := evdevAxis{Min: 0, Max: 255}
	hat := evdevAxis{Min: -1, Max: 1}
	ax := func(a evdevAxis, code uint16) evdevAxis { a.Code = code; return a }
	return evdevCaps{
		Name: "Microsoft X-Box 360 pad", Bus: evdev.BusUSB, Vendor: 0x045e, Product: 0x028e, Version: 0x0114,
		Keys: []uint16{0x130, 0x131, 0x133, 0x134, 0x136, 0x137, 0x13a, 0x13b, 0x13c, 0x13d, 0x13e},
		Abs: []evdevAxis{
			ax(stick, 0), ax(stick, 1), ax(trig, 2), ax(stick, 3), ax(stick, 4), ax(trig, 5), ax(hat, 0x10), ax(hat, 0x11),
		},
		Rumble: true,
	}
}

func TestIsGamepad(t *testing.T) {
	pad := xpadCaps()
	if !pad.isGamepad() {
		t.Error("xpad is not a gamepad")
	}
	mouse := evdevCaps{Keys: []uint16{0x110, 0x111, 0x112}, Abs: []evdevAxis{{Code: 0}, {Code: 1}}}
	touchpad := evdevCaps{Keys: []uint16{0x110, 0x145, 0x14a}, Abs: []evdevAxis{{Code: 0}, {Code: 1}}}
	keyboard := evdevCaps{Keys: []uint16{30, 48}}
	tablet := evdevCaps{Keys: []uint16{0x140, 0x14a}, Abs: []evdevAxis{{Code: 0}, {Code: 1}}}
	dpadOnly := evdevCaps{Keys: []uint16{0x120, 0x121}, Abs: []evdevAxis{{Code: 0x10}, {Code: 0x11}}}
	for name, c := range map[string]evdevCaps{"mouse": mouse, "touchpad": touchpad, "keyboard": keyboard, "tablet": tablet} {
		if c.isGamepad() {
			t.Errorf("%s accepted as a gamepad", name)
		}
	}
	if !dpadOnly.isGamepad() {
		t.Error("a joystick with only a hat is a gamepad")
	}
}

func TestEvdevLayoutOrder(t *testing.T) {
	c := xpadCaps()
	c.Keys = append([]uint16{1, 30}, c.Keys...)
	l := c.layout()
	if !slices.Equal(l.buttons[:3], []uint16{0x130, 0x131, 0x133}) || l.buttons[len(l.buttons)-2] != 1 {
		t.Errorf("buttons %x: joystick codes first, then the low ones", l.buttons)
	}
	if len(l.axes) != 6 || l.axes[2].Code != 2 || l.axes[3].Code != 3 {
		t.Errorf("axes %+v", l.axes)
	}
	if !slices.Equal(l.hats, []uint16{0x10}) {
		t.Errorf("hats %v", l.hats)
	}
}

func TestXpadMatchesSDLMapping(t *testing.T) {
	c := xpadCaps()
	l := c.layout()
	m := Mapping{GUID: c.info(1).GUID, Name: c.Name, Bindings: l.bindings(c.Vendor)}
	got := m.FormatSDLMapping()
	want := "a:b0,b:b1,back:b6,dpdown:h0.4,dpleft:h0.8,dpright:h0.2,dpup:h0.1,guide:b8," +
		"leftshoulder:b4,leftstick:b9,lefttrigger:a2,leftx:a0,lefty:a1,rightshoulder:b5,rightstick:b10," +
		"righttrigger:a5,rightx:a3,righty:a4,start:b7,x:b2,y:b3,"
	if !strings.Contains(got, want) {
		t.Errorf("mapping\n got %s\nwant it to hold %s", got, want)
	}
}

func TestSonySwapsFaceButtons(t *testing.T) {
	c := xpadCaps()
	l := c.layout()
	xbox, sony := l.bindings(0x045e), l.bindings(vendorSony)
	if xbox[InputX].Index != 2 || sony[InputX].Index != 3 || sony[InputY].Index != 2 {
		t.Errorf("X: xbox b%d sony b%d, Y: sony b%d", xbox[InputX].Index, sony[InputX].Index, sony[InputY].Index)
	}
}

func TestFillAndApply(t *testing.T) {
	c := xpadCaps()
	l := c.layout()
	m := Mapping{Bindings: l.bindings(c.Vendor)}
	keys := map[uint16]bool{0x130: true, 0x137: true}
	abs := map[uint16]int32{0: 32767, 1: -32768, 2: 255, 5: 0, 0x10: 1, 0x11: -1}
	var raw RawState
	l.fill(&raw, func(k uint16) bool { return keys[k] }, func(a uint16) int32 { return abs[a] })
	if raw.Hats[0] != HatRight|HatUp {
		t.Errorf("hat = %d", raw.Hats[0])
	}
	s := m.Apply(&raw)
	if s.Buttons != ButtonA|ButtonRightShoulder|ButtonDPadRight|ButtonDPadUp {
		t.Errorf("buttons %#x", s.Buttons)
	}
	if s.LX != 32767 || s.LY != 32767 {
		t.Errorf("left stick %d,%d: full right, and ABS_Y at its minimum is full up", s.LX, s.LY)
	}
	if s.LT != 255 || s.RT != 0 {
		t.Errorf("triggers %d,%d", s.LT, s.RT)
	}
}

func TestTriggerButtonsWithoutAnalogAxes(t *testing.T) {
	c := evdevCaps{
		Keys: []uint16{0x130, 0x138, 0x139},
		Abs:  []evdevAxis{{Code: 0, Min: 0, Max: 255}, {Code: 1, Min: 0, Max: 255}},
	}
	l := c.layout()
	b := l.bindings(0)
	if b[InputLeftTrigger].Kind != SourceButton || b[InputRightTrigger].Kind != SourceButton {
		t.Errorf("triggers %+v %+v", b[InputLeftTrigger], b[InputRightTrigger])
	}
	if b[InputDPadUp].Kind != SourceNone {
		t.Errorf("no D-pad on this device: %+v", b[InputDPadUp])
	}
}

func TestInfo(t *testing.T) {
	c := xpadCaps()
	info := c.info(7)
	if info.ID != 7 || info.Connection != ConnectionUSB || info.BatteryPct != -1 || !info.Connected || !ValidGUID(info.GUID) {
		t.Errorf("info %+v", info)
	}
	if info.GUID != "03000000"+"5e040000"+"8e020000"+"14010000" {
		t.Errorf("GUID %s", info.GUID)
	}
	c.Bus = evdev.BusBluetooth
	if c.info(1).Connection != ConnectionBluetooth {
		t.Error("bluetooth")
	}
}

func TestFFEffectBytes(t *testing.T) {
	b := ffEffectBytes(-1, motor(255), motor(128))
	if len(b) != evdev.FFEffectSize || ffEffectID(b) != -1 {
		t.Fatalf("effect %d bytes, id %d", len(b), ffEffectID(b))
	}
	if motor(255) != 0xFFFF {
		t.Errorf("motor(255) = %#x", motor(255))
	}
	if b[0] != evdev.FFRumble || b[1] != 0 {
		t.Errorf("type % x", b[:2])
	}
}
