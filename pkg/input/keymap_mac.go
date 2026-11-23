package input

// macKeys pairs HID usages with macOS virtual key codes (kVK_* in
// HIToolbox Events.h). Each side appears once.
var macKeys = [...]struct {
	usage uint16
	mac   uint16
}{
	{KeyA, 0x00}, {KeyS, 0x01}, {KeyD, 0x02}, {KeyF, 0x03}, {KeyH, 0x04}, {KeyG, 0x05},
	{KeyZ, 0x06}, {KeyX, 0x07}, {KeyC, 0x08}, {KeyV, 0x09}, {KeyNonUSBackslash, 0x0A},
	{KeyB, 0x0B}, {KeyQ, 0x0C}, {KeyW, 0x0D}, {KeyE, 0x0E}, {KeyR, 0x0F}, {KeyY, 0x10},
	{KeyT, 0x11}, {Key1, 0x12}, {Key2, 0x13}, {Key3, 0x14}, {Key4, 0x15}, {Key6, 0x16},
	{Key5, 0x17}, {KeyEqual, 0x18}, {Key9, 0x19}, {Key7, 0x1A}, {KeyMinus, 0x1B},
	{Key8, 0x1C}, {Key0, 0x1D}, {KeyRightBracket, 0x1E}, {KeyO, 0x1F}, {KeyU, 0x20},
	{KeyLeftBracket, 0x21}, {KeyI, 0x22}, {KeyP, 0x23}, {KeyEnter, 0x24}, {KeyL, 0x25},
	{KeyJ, 0x26}, {KeyQuote, 0x27}, {KeyK, 0x28}, {KeySemicolon, 0x29}, {KeyBackslash, 0x2A},
	{KeyComma, 0x2B}, {KeySlash, 0x2C}, {KeyN, 0x2D}, {KeyM, 0x2E}, {KeyPeriod, 0x2F},
	{KeyTab, 0x30}, {KeySpace, 0x31}, {KeyGrave, 0x32}, {KeyBackspace, 0x33},
	{KeyEscape, 0x35}, {KeyRightGUI, 0x36}, {KeyLeftGUI, 0x37}, {KeyLeftShift, 0x38},
	{KeyCapsLock, 0x39}, {KeyLeftAlt, 0x3A}, {KeyLeftControl, 0x3B}, {KeyRightShift, 0x3C},
	{KeyRightAlt, 0x3D}, {KeyRightControl, 0x3E}, {KeyF17, 0x40}, {KeyKPDecimal, 0x41},
	{KeyKPMultiply, 0x43}, {KeyKPPlus, 0x45}, {KeyNumLock, 0x47}, {KeyVolumeUp, 0x48},
	{KeyVolumeDown, 0x49}, {KeyMute, 0x4A}, {KeyKPDivide, 0x4B}, {KeyKPEnter, 0x4C},
	{KeyKPMinus, 0x4E}, {KeyF18, 0x4F}, {KeyF19, 0x50}, {KeyKPEqual, 0x51}, {KeyKP0, 0x52},
	{KeyKP1, 0x53}, {KeyKP2, 0x54}, {KeyKP3, 0x55}, {KeyKP4, 0x56}, {KeyKP5, 0x57},
	{KeyKP6, 0x58}, {KeyKP7, 0x59}, {KeyF20, 0x5A}, {KeyKP8, 0x5B}, {KeyKP9, 0x5C},
	{KeyInternational3, 0x5D}, {KeyInternational1, 0x5E}, {KeyKPComma, 0x5F},
	{KeyF5, 0x60}, {KeyF6, 0x61}, {KeyF7, 0x62}, {KeyF3, 0x63}, {KeyF8, 0x64}, {KeyF9, 0x65},
	{KeyLang2, 0x66}, {KeyF11, 0x67}, {KeyLang1, 0x68}, {KeyF13, 0x69}, {KeyF16, 0x6A},
	{KeyF14, 0x6B}, {KeyF10, 0x6D}, {KeyApplication, 0x6E}, {KeyF12, 0x6F}, {KeyF15, 0x71},
	{KeyInsert, 0x72}, {KeyHome, 0x73}, {KeyPageUp, 0x74}, {KeyDelete, 0x75}, {KeyF4, 0x76},
	{KeyEnd, 0x77}, {KeyF2, 0x78}, {KeyPageDown, 0x79}, {KeyF1, 0x7A}, {KeyLeft, 0x7B},
	{KeyRight, 0x7C}, {KeyDown, 0x7D}, {KeyUp, 0x7E},
}

const noKey = 0xFFFF

var macToHID, hidToMac [256]uint16

func init() {
	for i := range macToHID {
		macToHID[i], hidToMac[i] = noKey, noKey
	}
	for _, k := range macKeys {
		macToHID[k.mac] = k.usage
		hidToMac[k.usage] = k.mac
	}
}

// HIDFromMac maps a macOS virtual key code to a HID usage.
func HIDFromMac(keyCode uint16) (uint16, bool) {
	if keyCode >= 256 || macToHID[keyCode] == noKey {
		return 0, false
	}
	return macToHID[keyCode], true
}

// MacFromHID maps a HID usage to a macOS virtual key code.
func MacFromHID(usage uint16) (uint16, bool) {
	if usage >= 256 || hidToMac[usage] == noKey {
		return 0, false
	}
	return hidToMac[usage], true
}
