package input

import "testing"

func TestHIDUsageValues(t *testing.T) {
	cases := map[uint16]uint16{
		KeyA: 0x04, KeyZ: 0x1D, Key1: 0x1E, Key0: 0x27, KeyEnter: 0x28, KeyF1: 0x3A, KeyF12: 0x45,
		KeyPrintScreen: 0x46, KeyUp: 0x52, KeyKPEnter: 0x58, KeyKP0: 0x62, KeyNonUSBackslash: 0x64,
		KeyApplication: 0x65, KeyKPEqual: 0x67, KeyF13: 0x68, KeyF24: 0x73, KeyLeftControl: 0xE0, KeyRightGUI: 0xE7,
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("usage 0x%02x, want 0x%02x", got, want)
		}
	}
}

func TestMacKeyRoundTrip(t *testing.T) {
	seenUsage := map[uint16]bool{}
	seenMac := map[uint16]bool{}
	for _, k := range macKeys {
		if seenUsage[k.usage] || seenMac[k.mac] {
			t.Errorf("duplicate entry usage 0x%02x mac 0x%02x", k.usage, k.mac)
		}
		seenUsage[k.usage], seenMac[k.mac] = true, true
		mac, ok := MacFromHID(k.usage)
		if !ok || mac != k.mac {
			t.Errorf("MacFromHID(0x%02x) = 0x%02x, %v; want 0x%02x", k.usage, mac, ok, k.mac)
		}
		usage, ok := HIDFromMac(mac)
		if !ok || usage != k.usage {
			t.Errorf("HIDFromMac(0x%02x) = 0x%02x, %v; want 0x%02x", mac, usage, ok, k.usage)
		}
	}
	if len(macKeys) < 110 {
		t.Errorf("only %d mac keys mapped", len(macKeys))
	}
}

func TestWindowsKeyRoundTrip(t *testing.T) {
	type pair struct {
		vk  uint8
		ext bool
	}
	seenUsage := map[uint16]bool{}
	seenPair := map[pair]bool{}
	for _, k := range winKeys {
		p := pair{k.vk, k.extended}
		if seenUsage[k.usage] || seenPair[p] {
			t.Errorf("duplicate entry usage 0x%02x vk 0x%02x ext %v", k.usage, k.vk, k.extended)
		}
		seenUsage[k.usage], seenPair[p] = true, true
		vk, ext, ok := WindowsFromHID(k.usage)
		if !ok || vk != uint16(k.vk) || ext != k.extended {
			t.Errorf("WindowsFromHID(0x%02x) = 0x%02x %v %v; want 0x%02x %v", k.usage, vk, ext, ok, k.vk, k.extended)
		}
		usage, ok := HIDFromWindows(vk, ext)
		if !ok || usage != k.usage {
			t.Errorf("HIDFromWindows(0x%02x, %v) = 0x%02x, %v; want 0x%02x", vk, ext, usage, ok, k.usage)
		}
	}
	for _, a := range winAliases {
		if seenPair[pair{a.vk, a.extended}] {
			t.Errorf("alias vk 0x%02x ext %v shadows a primary entry", a.vk, a.extended)
		}
	}
	if len(winKeys) < 120 {
		t.Errorf("only %d windows keys mapped", len(winKeys))
	}
}

func TestWindowsAmbiguousKeys(t *testing.T) {
	cases := []struct {
		vk   uint16
		ext  bool
		want uint16
	}{
		{0x0D, false, KeyEnter},
		{0x0D, true, KeyKPEnter},
		{0x25, true, KeyLeft},
		{0x25, false, KeyKP4},
		{0x2E, true, KeyDelete},
		{0x2E, false, KeyKPDecimal},
		{0x0C, false, KeyKP5},
		{0x11, false, KeyLeftControl},
		{0x11, true, KeyRightControl},
		{0x12, true, KeyRightAlt},
		{0x10, false, KeyLeftShift},
		{0x90, true, KeyNumLock},
		{'A', true, KeyA},
	}
	for _, c := range cases {
		if got, ok := HIDFromWindows(c.vk, c.ext); !ok || got != c.want {
			t.Errorf("HIDFromWindows(0x%02x, %v) = 0x%02x, %v; want 0x%02x", c.vk, c.ext, got, ok, c.want)
		}
	}
}

func TestUnknownKeys(t *testing.T) {
	if _, ok := HIDFromMac(0x3F); ok {
		t.Error("kVK_Function has no HID usage on page 7")
	}
	if _, ok := HIDFromMac(0x1234); ok {
		t.Error("out-of-range mac code mapped")
	}
	if _, ok := MacFromHID(KeyPrintScreen); ok {
		t.Error("PrintScreen has no mac key")
	}
	if _, ok := HIDFromWindows(0x07, false); ok {
		t.Error("reserved VK 0x07 mapped")
	}
	if _, _, ok := WindowsFromHID(0x300); ok {
		t.Error("out-of-range usage mapped")
	}
}

func TestWindowsScanCodes(t *testing.T) {
	type sc struct {
		scan uint8
		ext  bool
	}
	seen := map[sc]uint16{}
	vkOnly := map[uint16]bool{KeyPause: true, KeyLang1: true, KeyLang2: true}
	for _, k := range winKeys {
		scan, ext, vk, ok := windowsScan(k.usage)
		if !ok || vk != k.vk || ext != k.extended {
			t.Fatalf("windowsScan(0x%02x) = %x %v %x %v", k.usage, scan, ext, vk, ok)
		}
		if scan == 0 {
			if !vkOnly[k.usage] {
				t.Errorf("usage 0x%02x has no scan code", k.usage)
			}
			continue
		}
		if prev, dup := seen[sc{scan, ext}]; dup {
			t.Errorf("usages 0x%02x and 0x%02x share scan 0x%02x ext %v", prev, k.usage, scan, ext)
		}
		seen[sc{scan, ext}] = k.usage
	}
	spot := map[uint16]sc{KeyA: {0x1E, false}, KeyKPEnter: {0x1C, true}, KeyEnter: {0x1C, false},
		KeyRightAlt: {0x38, true}, KeyNumLock: {0x45, true}, KeyF24: {0x76, false}, KeyUp: {0x48, true}, KeyKP8: {0x48, false}}
	for u, want := range spot {
		if scan, ext, _, _ := windowsScan(u); (sc{scan, ext}) != want {
			t.Errorf("windowsScan(0x%02x) = %x/%v, want %x/%v", u, scan, ext, want.scan, want.ext)
		}
	}
}
