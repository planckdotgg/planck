package input

import "testing"

func TestEvdevKeyRoundTrip(t *testing.T) {
	seenUsage := map[uint16]bool{}
	seenCode := map[uint16]bool{}
	for _, k := range evdevKeys {
		if seenUsage[k.usage] || seenCode[k.code] {
			t.Errorf("duplicate entry usage 0x%02x code %d", k.usage, k.code)
		}
		seenUsage[k.usage], seenCode[k.code] = true, true
		code, ok := EvdevFromHID(k.usage)
		if !ok || code != k.code {
			t.Errorf("EvdevFromHID(0x%02x) = %d, %v; want %d", k.usage, code, ok, k.code)
		}
		usage, ok := HIDFromEvdev(code)
		if !ok || usage != k.usage {
			t.Errorf("HIDFromEvdev(%d) = 0x%02x, %v; want 0x%02x", code, usage, ok, k.usage)
		}
	}
	if len(evdevKeys) < 120 {
		t.Errorf("only %d evdev keys mapped", len(evdevKeys))
	}
}

func TestEvdevKeysCoverEveryMacKey(t *testing.T) {
	for _, k := range macKeys {
		if _, ok := EvdevFromHID(k.usage); !ok {
			t.Errorf("HID usage 0x%02x (mac key 0x%02x) has no evdev code", k.usage, k.mac)
		}
	}
}

func TestEvdevSpotChecks(t *testing.T) {
	cases := map[uint16]uint16{
		KeyA: 30, KeyEscape: 1, KeyEnter: 28, KeySpace: 57, KeyLeftControl: 29, KeyRightAlt: 100,
		KeyLeftGUI: 125, KeyF11: 87, KeyUp: 103, KeyKPEnter: 96, KeyPrintScreen: 99, KeyF24: 194,
	}
	for usage, want := range cases {
		if got, ok := EvdevFromHID(usage); !ok || got != want {
			t.Errorf("EvdevFromHID(0x%02x) = %d, %v; want %d", usage, got, ok, want)
		}
	}
	if u, ok := HIDFromEvdev(43); !ok || u != KeyBackslash {
		t.Errorf("KEY_BACKSLASH = 0x%02x, %v; want Backslash", u, ok)
	}
}

func TestEvdevUnknownKeys(t *testing.T) {
	if _, ok := EvdevFromHID(KeyNonUSHash); ok {
		t.Error("non-US # shares KEY_BACKSLASH and must stay unmapped")
	}
	if _, ok := EvdevFromHID(0x300); ok {
		t.Error("out-of-range usage mapped")
	}
	if _, ok := HIDFromEvdev(0); ok {
		t.Error("KEY_RESERVED mapped")
	}
	if _, ok := HIDFromEvdev(0x1234); ok {
		t.Error("out-of-range code mapped")
	}
}
