package evdev

import "testing"

func TestIoctlNumbers(t *testing.T) {
	cases := []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{"EVIOCGID", EVIOCGID, 0x80084502},
		{"EVIOCGNAME(256)", EVIOCGNAME(256), 0x81004506},
		{"EVIOCGABS(ABS_X)", EVIOCGABS(AbsX), 0x80184540},
		{"EVIOCGBIT(EV_KEY, 96)", EVIOCGBIT(EvKey, 96), 0x80604521},
		{"EVIOCGKEY(96)", EVIOCGKEY(96), 0x80604518},
		{"EVIOCRMFF", EVIOCRMFF, 0x40044581},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %#x, want %#x", c.name, c.got, c.want)
		}
	}
	if FFEffectSize != 48 && FFEffectSize != 44 {
		t.Errorf("FFEffectSize = %d", FFEffectSize)
	}
}

func TestEventRoundTrip(t *testing.T) {
	want := Event{Type: EvAbs, Code: AbsRY, Value: -32768}
	b := Encode(nil, want, Event{Type: EvSyn})
	if len(b) != 2*EventSize {
		t.Fatalf("encoded %d bytes, want %d", len(b), 2*EventSize)
	}
	if got := ParseEvent(b); got != want {
		t.Errorf("parsed %+v, want %+v", got, want)
	}
	for i := 0; i < EventSize-8; i++ {
		if b[i] != 0 {
			t.Fatalf("time byte %d = %d, want 0", i, b[i])
		}
	}
}

func TestTestBit(t *testing.T) {
	bm := []byte{0x00, 0x04}
	if !TestBit(bm, 10) || TestBit(bm, 9) || TestBit(bm, 100) {
		t.Error("TestBit")
	}
}
