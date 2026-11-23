package input

type winKey struct {
	usage    uint16
	vk       uint8
	extended bool  // the key sends an E0-prefixed scan code
	scan     uint8 // set 1 make code, without the E0 prefix; 0 for VK-only keys
}

// winKeys pairs HID usages with Windows virtual-key codes and US-layout scan
// codes. A VK alone is ambiguous for keys that share one (Enter and keypad
// Enter); the extended bit separates them. Each (vk, extended) pair and each
// usage appears once. Hosts inject by scan code, which names a physical key
// whatever the host's keyboard layout, as HID usages do; Pause (an E1
// sequence) and the IME keys have no usable make code and go by VK.
var winKeys = [...]winKey{
	{KeyA, 'A', false, 0x1E}, {KeyB, 'B', false, 0x30}, {KeyC, 'C', false, 0x2E}, {KeyD, 'D', false, 0x20},
	{KeyE, 'E', false, 0x12}, {KeyF, 'F', false, 0x21}, {KeyG, 'G', false, 0x22}, {KeyH, 'H', false, 0x23},
	{KeyI, 'I', false, 0x17}, {KeyJ, 'J', false, 0x24}, {KeyK, 'K', false, 0x25}, {KeyL, 'L', false, 0x26},
	{KeyM, 'M', false, 0x32}, {KeyN, 'N', false, 0x31}, {KeyO, 'O', false, 0x18}, {KeyP, 'P', false, 0x19},
	{KeyQ, 'Q', false, 0x10}, {KeyR, 'R', false, 0x13}, {KeyS, 'S', false, 0x1F}, {KeyT, 'T', false, 0x14},
	{KeyU, 'U', false, 0x16}, {KeyV, 'V', false, 0x2F}, {KeyW, 'W', false, 0x11}, {KeyX, 'X', false, 0x2D},
	{KeyY, 'Y', false, 0x15}, {KeyZ, 'Z', false, 0x2C},
	{Key1, '1', false, 0x02}, {Key2, '2', false, 0x03}, {Key3, '3', false, 0x04}, {Key4, '4', false, 0x05},
	{Key5, '5', false, 0x06}, {Key6, '6', false, 0x07}, {Key7, '7', false, 0x08}, {Key8, '8', false, 0x09},
	{Key9, '9', false, 0x0A}, {Key0, '0', false, 0x0B},
	{KeyEnter, 0x0D, false, 0x1C}, {KeyEscape, 0x1B, false, 0x01}, {KeyBackspace, 0x08, false, 0x0E},
	{KeyTab, 0x09, false, 0x0F}, {KeySpace, 0x20, false, 0x39}, {KeyMinus, 0xBD, false, 0x0C},
	{KeyEqual, 0xBB, false, 0x0D}, {KeyLeftBracket, 0xDB, false, 0x1A}, {KeyRightBracket, 0xDD, false, 0x1B},
	{KeyBackslash, 0xDC, false, 0x2B}, {KeySemicolon, 0xBA, false, 0x27}, {KeyQuote, 0xDE, false, 0x28},
	{KeyGrave, 0xC0, false, 0x29}, {KeyComma, 0xBC, false, 0x33}, {KeyPeriod, 0xBE, false, 0x34},
	{KeySlash, 0xBF, false, 0x35}, {KeyCapsLock, 0x14, false, 0x3A},
	{KeyF1, 0x70, false, 0x3B}, {KeyF2, 0x71, false, 0x3C}, {KeyF3, 0x72, false, 0x3D}, {KeyF4, 0x73, false, 0x3E},
	{KeyF5, 0x74, false, 0x3F}, {KeyF6, 0x75, false, 0x40}, {KeyF7, 0x76, false, 0x41}, {KeyF8, 0x77, false, 0x42},
	{KeyF9, 0x78, false, 0x43}, {KeyF10, 0x79, false, 0x44}, {KeyF11, 0x7A, false, 0x57}, {KeyF12, 0x7B, false, 0x58},
	{KeyF13, 0x7C, false, 0x64}, {KeyF14, 0x7D, false, 0x65}, {KeyF15, 0x7E, false, 0x66}, {KeyF16, 0x7F, false, 0x67},
	{KeyF17, 0x80, false, 0x68}, {KeyF18, 0x81, false, 0x69}, {KeyF19, 0x82, false, 0x6A}, {KeyF20, 0x83, false, 0x6B},
	{KeyF21, 0x84, false, 0x6C}, {KeyF22, 0x85, false, 0x6D}, {KeyF23, 0x86, false, 0x6E}, {KeyF24, 0x87, false, 0x76},
	{KeyPrintScreen, 0x2C, true, 0x37}, {KeyScrollLock, 0x91, false, 0x46}, {KeyPause, 0x13, false, 0x00},
	{KeyInsert, 0x2D, true, 0x52}, {KeyHome, 0x24, true, 0x47}, {KeyPageUp, 0x21, true, 0x49},
	{KeyDelete, 0x2E, true, 0x53}, {KeyEnd, 0x23, true, 0x4F}, {KeyPageDown, 0x22, true, 0x51},
	{KeyRight, 0x27, true, 0x4D}, {KeyLeft, 0x25, true, 0x4B}, {KeyDown, 0x28, true, 0x50}, {KeyUp, 0x26, true, 0x48},
	{KeyNumLock, 0x90, true, 0x45}, {KeyKPDivide, 0x6F, true, 0x35}, {KeyKPMultiply, 0x6A, false, 0x37},
	{KeyKPMinus, 0x6D, false, 0x4A}, {KeyKPPlus, 0x6B, false, 0x4E}, {KeyKPEnter, 0x0D, true, 0x1C},
	{KeyKP1, 0x61, false, 0x4F}, {KeyKP2, 0x62, false, 0x50}, {KeyKP3, 0x63, false, 0x51}, {KeyKP4, 0x64, false, 0x4B},
	{KeyKP5, 0x65, false, 0x4C}, {KeyKP6, 0x66, false, 0x4D}, {KeyKP7, 0x67, false, 0x47}, {KeyKP8, 0x68, false, 0x48},
	{KeyKP9, 0x69, false, 0x49}, {KeyKP0, 0x60, false, 0x52}, {KeyKPDecimal, 0x6E, false, 0x53},
	{KeyNonUSBackslash, 0xE2, false, 0x56}, {KeyApplication, 0x5D, true, 0x5D},
	{KeyMute, 0xAD, true, 0x20}, {KeyVolumeDown, 0xAE, true, 0x2E}, {KeyVolumeUp, 0xAF, true, 0x30},
	{KeyInternational4, 0x1C, false, 0x79}, {KeyInternational5, 0x1D, false, 0x7B},
	{KeyLang1, 0x15, false, 0x00}, {KeyLang2, 0x19, false, 0x00},
	{KeyLeftControl, 0xA2, false, 0x1D}, {KeyLeftShift, 0xA0, false, 0x2A}, {KeyLeftAlt, 0xA4, false, 0x38},
	{KeyLeftGUI, 0x5B, true, 0x5B}, {KeyRightControl, 0xA3, true, 0x1D}, {KeyRightShift, 0xA1, false, 0x36},
	{KeyRightAlt, 0xA5, true, 0x38}, {KeyRightGUI, 0x5C, true, 0x5C},
}

// winAliases are VK reports with no HID-to-VK counterpart: navigation VKs
// that keypad keys send with NumLock off (no E0 prefix), and the generic
// modifier VKs that some input APIs report.
var winAliases = [...]winKey{
	{KeyKP0, 0x2D, false, 0}, {KeyKPDecimal, 0x2E, false, 0}, {KeyKP1, 0x23, false, 0},
	{KeyKP2, 0x28, false, 0}, {KeyKP3, 0x22, false, 0}, {KeyKP4, 0x25, false, 0},
	{KeyKP5, 0x0C, false, 0}, {KeyKP6, 0x27, false, 0}, {KeyKP7, 0x24, false, 0},
	{KeyKP8, 0x26, false, 0}, {KeyKP9, 0x21, false, 0},
	{KeyLeftShift, 0x10, false, 0}, {KeyLeftControl, 0x11, false, 0}, {KeyRightControl, 0x11, true, 0},
	{KeyLeftAlt, 0x12, false, 0}, {KeyRightAlt, 0x12, true, 0},
}

var (
	winToHID [2][256]uint16 // [extended][vk]
	hidToWin [256]winKey
)

func init() {
	for e := range winToHID {
		for vk := range winToHID[e] {
			winToHID[e][vk] = noKey
		}
	}
	for i := range hidToWin {
		hidToWin[i].usage = noKey
	}
	ext := func(b bool) int {
		if b {
			return 1
		}
		return 0
	}
	for _, k := range winKeys {
		winToHID[ext(k.extended)][k.vk] = k.usage
		hidToWin[k.usage] = k
	}
	for _, k := range winAliases {
		winToHID[ext(k.extended)][k.vk] = k.usage
	}
}

// HIDFromWindows maps a Windows virtual-key code and its extended-key flag to
// a HID usage. When the exact pair is unknown, the flag is ignored, since
// APIs disagree on it for some keys (NumLock, Pause).
func HIDFromWindows(vk uint16, extended bool) (uint16, bool) {
	if vk >= 256 {
		return 0, false
	}
	e := 0
	if extended {
		e = 1
	}
	if u := winToHID[e][vk]; u != noKey {
		return u, true
	}
	if u := winToHID[1-e][vk]; u != noKey {
		return u, true
	}
	return 0, false
}

// WindowsFromHID maps a HID usage to a Windows virtual-key code and whether
// the key must be sent as an extended key.
func WindowsFromHID(usage uint16) (vk uint16, extended bool, ok bool) {
	if usage >= 256 || hidToWin[usage].usage == noKey {
		return 0, false, false
	}
	k := hidToWin[usage]
	return uint16(k.vk), k.extended, true
}

// windowsScan returns the scan code a host injects for usage. scan is 0 for
// keys that must be sent by VK.
func windowsScan(usage uint16) (scan uint8, extended bool, vk uint8, ok bool) {
	if usage >= 256 || hidToWin[usage].usage == noKey {
		return 0, false, 0, false
	}
	k := hidToWin[usage]
	return k.scan, k.extended, k.vk, true
}
