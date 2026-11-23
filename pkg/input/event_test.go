package input

import (
	"bytes"
	"errors"
	"testing"
)

var sampleEvents = []Event{
	{Type: TypeKey, Key: KeyEvent{Usage: KeyA, Pressed: true, Modifiers: ModLeftShift | ModRightGUI, SystemKeyCapture: true}},
	{Type: TypeKey, Key: KeyEvent{Usage: KeyRightGUI}},
	{Type: TypeMouseMoveAbs, MouseAbs: MouseMoveAbs{X: 0, Y: 65535}},
	{Type: TypeMouseMoveRel, MouseRel: MouseMoveRel{DX: -32768, DY: 32767}},
	{Type: TypeMouseButton, Button: MouseButton{Button: ButtonX2, Pressed: true}},
	{Type: TypeMouseWheel, Wheel: MouseWheel{DX: -WheelNotch, DY: 3 * WheelNotch}},
	{Type: TypeGamepad, Gamepad: GamepadState{Index: 15, Buttons: PadA | PadGuide | PadDPadLeft, LeftTrigger: 255,
		RightTrigger: 1, LeftX: -32768, LeftY: 32767, RightX: -1, RightY: 0}},
	{Type: TypeRumble, Rumble: RumbleEvent{Index: 3, LowFreq: 65535, HighFreq: 1, DurationMs: 500}},
}

func TestEventRoundTrip(t *testing.T) {
	for _, ev := range sampleEvents {
		var buf [MaxEventSize]byte
		n, err := ev.Encode(buf[:])
		if err != nil {
			t.Fatalf("encode %+v: %v", ev, err)
		}
		var got Event
		m, err := got.Decode(buf[:n])
		if err != nil || m != n {
			t.Fatalf("decode %+v = %d, %v; want %d bytes", ev, m, err, n)
		}
		if got != ev {
			t.Fatalf("round trip\n got %+v\nwant %+v", got, ev)
		}
	}
}

func TestEventWireBytes(t *testing.T) {
	cases := []struct {
		ev   Event
		want []byte
	}{
		{Event{Type: TypeKey, Key: KeyEvent{Usage: KeyEnter, Pressed: true, Modifiers: ModLeftControl}},
			[]byte{1, 0x28, 0x00, 0x01, 0x01}},
		{Event{Type: TypeMouseMoveRel, MouseRel: MouseMoveRel{DX: -2, DY: 258}},
			[]byte{3, 0xFE, 0xFF, 0x02, 0x01}},
		{Event{Type: TypeGamepad, Gamepad: GamepadState{Index: 1, Buttons: PadY, LeftTrigger: 9, RightTrigger: 8, LeftX: 1, LeftY: -1, RightX: 256, RightY: 2}},
			[]byte{6, 1, 0x00, 0x80, 0x00, 0x00, 9, 8, 0x01, 0x00, 0xFF, 0xFF, 0x00, 0x01, 0x02, 0x00}},
		{Event{Type: TypeRumble, Rumble: RumbleEvent{Index: 2, LowFreq: 0x1234, HighFreq: 0xABCD, DurationMs: 100}},
			[]byte{7, 2, 0x34, 0x12, 0xCD, 0xAB, 100, 0}},
	}
	for _, c := range cases {
		var buf [MaxEventSize]byte
		n, err := c.ev.Encode(buf[:])
		if err != nil || !bytes.Equal(buf[:n], c.want) {
			t.Errorf("encode %+v = % x, %v; want % x", c.ev, buf[:n], err, c.want)
		}
	}
}

func TestEventsShareOneDatagram(t *testing.T) {
	var buf []byte
	for _, ev := range sampleEvents {
		var b [MaxEventSize]byte
		n, _ := ev.Encode(b[:])
		buf = append(buf, b[:n]...)
	}
	for i := 0; len(buf) > 0; i++ {
		var ev Event
		n, err := ev.Decode(buf)
		if err != nil {
			t.Fatalf("event %d: %v", i, err)
		}
		if ev != sampleEvents[i] {
			t.Fatalf("event %d = %+v, want %+v", i, ev, sampleEvents[i])
		}
		buf = buf[n:]
	}
}

func TestDecodeRejectsBadInput(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want error
	}{
		{"empty", nil, ErrShortBuffer},
		{"unknown type", []byte{0, 0, 0, 0, 0}, ErrInvalid},
		{"type past range", []byte{8, 0, 0}, ErrInvalid},
		{"truncated key", []byte{1, 4, 0, 1}, ErrShortBuffer},
		{"unknown key flag", []byte{1, 4, 0, 0x04, 0}, ErrInvalid},
		{"button zero", []byte{4, 0, 1}, ErrInvalid},
		{"button six", []byte{4, 6, 1}, ErrInvalid},
		{"button state 2", []byte{4, 1, 2}, ErrInvalid},
		{"gamepad index", append([]byte{6, MaxGamepads}, make([]byte, 14)...), ErrInvalid},
		{"rumble index", []byte{7, 200, 0, 0, 0, 0, 0, 0}, ErrInvalid},
	}
	for _, c := range cases {
		var ev Event
		if _, err := ev.Decode(c.in); !errors.Is(err, c.want) {
			t.Errorf("%s: err %v, want %v", c.name, err, c.want)
		}
	}
}

func TestEncodeRejectsBadInput(t *testing.T) {
	var buf [MaxEventSize]byte
	bad := []Event{
		{},
		{Type: 99},
		{Type: TypeMouseButton, Button: MouseButton{Button: 0}},
		{Type: TypeGamepad, Gamepad: GamepadState{Index: MaxGamepads}},
	}
	for _, ev := range bad {
		if _, err := ev.Encode(buf[:]); !errors.Is(err, ErrInvalid) {
			t.Errorf("encode %+v: err %v, want ErrInvalid", ev, err)
		}
	}
	ev := sampleEvents[6]
	if _, err := ev.Encode(buf[:4]); !errors.Is(err, ErrShortBuffer) {
		t.Errorf("short buffer: err %v", err)
	}
}

func TestEventCodecDoesNotAllocate(t *testing.T) {
	var buf [MaxEventSize]byte
	var out Event
	allocs := testing.AllocsPerRun(100, func() {
		for i := range sampleEvents {
			n, err := sampleEvents[i].Encode(buf[:])
			if err != nil {
				t.Fatal(err)
			}
			if _, err := out.Decode(buf[:n]); err != nil {
				t.Fatal(err)
			}
		}
	})
	if allocs != 0 {
		t.Fatalf("%v allocations per pass, want 0", allocs)
	}
}

func FuzzDecodeEncode(f *testing.F) {
	for _, ev := range sampleEvents {
		var b [MaxEventSize]byte
		n, _ := ev.Encode(b[:])
		f.Add(b[:n])
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		var ev Event
		n, err := ev.Decode(in)
		if err != nil {
			return
		}
		var out [MaxEventSize]byte
		m, err := ev.Encode(out[:])
		if err != nil {
			t.Fatalf("decoded %+v does not re-encode: %v", ev, err)
		}
		if !bytes.Equal(out[:m], in[:n]) {
			t.Fatalf("re-encode % x, input % x", out[:m], in[:n])
		}
	})
}
