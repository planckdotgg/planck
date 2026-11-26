package uinput

import (
	"encoding/binary"
	"testing"

	"github.com/planckdotgg/planck/internal/linux/evdev"
)

func TestIoctlNumbers(t *testing.T) {
	cases := []struct {
		name      string
		got, want uintptr
	}{
		{"UI_DEV_CREATE", ioctlDevCreate, 0x5501},
		{"UI_DEV_DESTROY", ioctlDevDestroy, 0x5502},
		{"UI_DEV_SETUP", ioctlDevSetup, 0x405c5503},
		{"UI_ABS_SETUP", ioctlAbsSetup, 0x401c5504},
		{"UI_SET_EVBIT", ioctlSetEvBit, 0x40045564},
		{"UI_SET_KEYBIT", ioctlSetKeyBit, 0x40045565},
		{"UI_SET_RELBIT", ioctlSetRelBit, 0x40045566},
		{"UI_SET_ABSBIT", ioctlSetAbsBit, 0x40045567},
		{"UI_SET_FFBIT", ioctlSetFFBit, 0x4004556b},
		{"UI_SET_PROPBIT", ioctlSetPropBit, 0x4004556e},
		{"UI_END_FF_ERASE", ioctlEndFFErase, 0x400c55cb},
		{"UI_BEGIN_FF_ERASE", ioctlBeginFFErase, 0xc00c55ca},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %#x, want %#x", c.name, c.got, c.want)
		}
	}
	if evdev.FFEffectSize == 48 {
		if ioctlBeginFFUpload != 0xc06855c8 || ioctlEndFFUpload != 0x406855c9 {
			t.Errorf("ff upload ioctls %#x %#x", ioctlBeginFFUpload, ioctlEndFFUpload)
		}
	}
}

func TestSetupBytes(t *testing.T) {
	cfg := Config{Name: "Planck Test", Bus: evdev.BusUSB, Vendor: 0x045e, Product: 0x028e, Version: 0x0110, Rumble: true}
	b := cfg.setupBytes()
	if len(b) != 92 {
		t.Fatalf("setup is %d bytes", len(b))
	}
	if binary.NativeEndian.Uint16(b[2:]) != 0x045e || binary.NativeEndian.Uint16(b[4:]) != 0x028e {
		t.Errorf("vendor/product % x", b[:8])
	}
	if string(b[8:8+11]) != "Planck Test" || b[8+11] != 0 {
		t.Errorf("name %q", b[8:8+16])
	}
	if got := binary.NativeEndian.Uint32(b[88:]); got != FFEffectsMax {
		t.Errorf("ff_effects_max = %d", got)
	}
	long := Config{Name: string(make([]byte, 200))}
	if lb := long.setupBytes(); len(lb) != 92 {
		t.Errorf("long name grew the struct to %d", len(lb))
	}
}

func TestAbsSetupBytes(t *testing.T) {
	b := absSetupBytes(AbsAxis{Code: evdev.AbsRX, Min: -32768, Max: 32767, Fuzz: 16, Flat: 128})
	if len(b) != 28 || binary.NativeEndian.Uint16(b) != evdev.AbsRX {
		t.Fatalf("% x", b)
	}
	if int32(binary.NativeEndian.Uint32(b[8:])) != -32768 || int32(binary.NativeEndian.Uint32(b[12:])) != 32767 ||
		binary.NativeEndian.Uint32(b[16:]) != 16 || binary.NativeEndian.Uint32(b[20:]) != 128 {
		t.Errorf("absinfo % x", b[4:])
	}
}

func TestParseUpload(t *testing.T) {
	up := make([]byte, ffUploadSize)
	binary.NativeEndian.PutUint16(up[8:], evdev.FFRumble)
	binary.NativeEndian.PutUint16(up[10:], 3)
	binary.NativeEndian.PutUint16(up[8+evdev.FFRumbleOffset:], 0xF000)
	binary.NativeEndian.PutUint16(up[8+evdev.FFRumbleOffset+2:], 0x0123)
	id, strong, weak, ok := parseUpload(up)
	if !ok || id != 3 || strong != 0xF000 || weak != 0x0123 {
		t.Errorf("parseUpload = %d %#x %#x %v", id, strong, weak, ok)
	}
	binary.NativeEndian.PutUint16(up[8:], 0x52)
	if _, _, _, ok := parseUpload(up); ok {
		t.Error("periodic effect accepted")
	}
}
