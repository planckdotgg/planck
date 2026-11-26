package input

// evdevKeys pairs HID usages with Linux evdev key codes (KEY_* in
// linux/input-event-codes.h). Each side appears once; the HID "non-US #"
// usage has no entry because Linux reports it as KEY_BACKSLASH too.
var evdevKeys = [...]struct {
	usage uint16
	code  uint16
}{
	{KeyA, 30}, {KeyB, 48}, {KeyC, 46}, {KeyD, 32}, {KeyE, 18}, {KeyF, 33}, {KeyG, 34},
	{KeyH, 35}, {KeyI, 23}, {KeyJ, 36}, {KeyK, 37}, {KeyL, 38}, {KeyM, 50}, {KeyN, 49},
	{KeyO, 24}, {KeyP, 25}, {KeyQ, 16}, {KeyR, 19}, {KeyS, 31}, {KeyT, 20}, {KeyU, 22},
	{KeyV, 47}, {KeyW, 17}, {KeyX, 45}, {KeyY, 21}, {KeyZ, 44},
	{Key1, 2}, {Key2, 3}, {Key3, 4}, {Key4, 5}, {Key5, 6}, {Key6, 7}, {Key7, 8}, {Key8, 9},
	{Key9, 10}, {Key0, 11},
	{KeyEnter, 28}, {KeyEscape, 1}, {KeyBackspace, 14}, {KeyTab, 15}, {KeySpace, 57},
	{KeyMinus, 12}, {KeyEqual, 13}, {KeyLeftBracket, 26}, {KeyRightBracket, 27},
	{KeyBackslash, 43}, {KeySemicolon, 39}, {KeyQuote, 40}, {KeyGrave, 41}, {KeyComma, 51},
	{KeyPeriod, 52}, {KeySlash, 53}, {KeyCapsLock, 58},
	{KeyF1, 59}, {KeyF2, 60}, {KeyF3, 61}, {KeyF4, 62}, {KeyF5, 63}, {KeyF6, 64}, {KeyF7, 65},
	{KeyF8, 66}, {KeyF9, 67}, {KeyF10, 68}, {KeyF11, 87}, {KeyF12, 88},
	{KeyPrintScreen, 99}, {KeyScrollLock, 70}, {KeyPause, 119}, {KeyInsert, 110}, {KeyHome, 102},
	{KeyPageUp, 104}, {KeyDelete, 111}, {KeyEnd, 107}, {KeyPageDown, 109},
	{KeyRight, 106}, {KeyLeft, 105}, {KeyDown, 108}, {KeyUp, 103},
	{KeyNumLock, 69}, {KeyKPDivide, 98}, {KeyKPMultiply, 55}, {KeyKPMinus, 74}, {KeyKPPlus, 78},
	{KeyKPEnter, 96}, {KeyKP1, 79}, {KeyKP2, 80}, {KeyKP3, 81}, {KeyKP4, 75}, {KeyKP5, 76},
	{KeyKP6, 77}, {KeyKP7, 71}, {KeyKP8, 72}, {KeyKP9, 73}, {KeyKP0, 82}, {KeyKPDecimal, 83},
	{KeyNonUSBackslash, 86}, {KeyApplication, 127}, {KeyPower, 116}, {KeyKPEqual, 117},
	{KeyF13, 183}, {KeyF14, 184}, {KeyF15, 185}, {KeyF16, 186}, {KeyF17, 187}, {KeyF18, 188},
	{KeyF19, 189}, {KeyF20, 190}, {KeyF21, 191}, {KeyF22, 192}, {KeyF23, 193}, {KeyF24, 194},
	{KeyMute, 113}, {KeyVolumeUp, 115}, {KeyVolumeDown, 114}, {KeyKPComma, 121},
	{KeyInternational1, 89}, {KeyInternational3, 124}, {KeyInternational4, 92},
	{KeyInternational5, 94}, {KeyLang1, 122}, {KeyLang2, 123},
	{KeyLeftControl, 29}, {KeyLeftShift, 42}, {KeyLeftAlt, 56}, {KeyLeftGUI, 125},
	{KeyRightControl, 97}, {KeyRightShift, 54}, {KeyRightAlt, 100}, {KeyRightGUI, 126},
}

const evdevKeyMax = 256

var evdevToHID, hidToEvdev [evdevKeyMax]uint16

func init() {
	for i := range evdevToHID {
		evdevToHID[i], hidToEvdev[i] = noKey, noKey
	}
	for _, k := range evdevKeys {
		evdevToHID[k.code] = k.usage
		hidToEvdev[k.usage] = k.code
	}
}

// HIDFromEvdev maps a Linux evdev key code to a HID usage.
func HIDFromEvdev(code uint16) (uint16, bool) {
	if code >= evdevKeyMax || evdevToHID[code] == noKey {
		return 0, false
	}
	return evdevToHID[code], true
}

// EvdevFromHID maps a HID usage to a Linux evdev key code.
func EvdevFromHID(usage uint16) (uint16, bool) {
	if usage >= evdevKeyMax || hidToEvdev[usage] == noKey {
		return 0, false
	}
	return hidToEvdev[usage], true
}
