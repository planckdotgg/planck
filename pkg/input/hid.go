package input

// Key codes are USB HID usages from the Keyboard/Keypad page (0x07), the one
// key vocabulary every client and host maps through.
const (
	KeyA uint16 = 0x04 + iota
	KeyB
	KeyC
	KeyD
	KeyE
	KeyF
	KeyG
	KeyH
	KeyI
	KeyJ
	KeyK
	KeyL
	KeyM
	KeyN
	KeyO
	KeyP
	KeyQ
	KeyR
	KeyS
	KeyT
	KeyU
	KeyV
	KeyW
	KeyX
	KeyY
	KeyZ
	Key1
	Key2
	Key3
	Key4
	Key5
	Key6
	Key7
	Key8
	Key9
	Key0
	KeyEnter
	KeyEscape
	KeyBackspace
	KeyTab
	KeySpace
	KeyMinus
	KeyEqual
	KeyLeftBracket
	KeyRightBracket
	KeyBackslash
	KeyNonUSHash
	KeySemicolon
	KeyQuote
	KeyGrave
	KeyComma
	KeyPeriod
	KeySlash
	KeyCapsLock
	KeyF1
	KeyF2
	KeyF3
	KeyF4
	KeyF5
	KeyF6
	KeyF7
	KeyF8
	KeyF9
	KeyF10
	KeyF11
	KeyF12
	KeyPrintScreen
	KeyScrollLock
	KeyPause
	KeyInsert
	KeyHome
	KeyPageUp
	KeyDelete
	KeyEnd
	KeyPageDown
	KeyRight
	KeyLeft
	KeyDown
	KeyUp
	KeyNumLock
	KeyKPDivide
	KeyKPMultiply
	KeyKPMinus
	KeyKPPlus
	KeyKPEnter
	KeyKP1
	KeyKP2
	KeyKP3
	KeyKP4
	KeyKP5
	KeyKP6
	KeyKP7
	KeyKP8
	KeyKP9
	KeyKP0
	KeyKPDecimal
	KeyNonUSBackslash
	KeyApplication
	KeyPower
	KeyKPEqual
	KeyF13
	KeyF14
	KeyF15
	KeyF16
	KeyF17
	KeyF18
	KeyF19
	KeyF20
	KeyF21
	KeyF22
	KeyF23
	KeyF24
)

const (
	KeyMute           uint16 = 0x7F
	KeyVolumeUp       uint16 = 0x80
	KeyVolumeDown     uint16 = 0x81
	KeyKPComma        uint16 = 0x85
	KeyInternational1 uint16 = 0x87 // JIS Ro
	KeyInternational3 uint16 = 0x89 // JIS Yen
	KeyInternational4 uint16 = 0x8A // JIS Henkan
	KeyInternational5 uint16 = 0x8B // JIS Muhenkan
	KeyLang1          uint16 = 0x90 // Hangul, Mac Kana
	KeyLang2          uint16 = 0x91 // Hanja, Mac Eisu
	KeyLeftControl    uint16 = 0xE0
	KeyLeftShift      uint16 = 0xE1
	KeyLeftAlt        uint16 = 0xE2
	KeyLeftGUI        uint16 = 0xE3
	KeyRightControl   uint16 = 0xE4
	KeyRightShift     uint16 = 0xE5
	KeyRightAlt       uint16 = 0xE6
	KeyRightGUI       uint16 = 0xE7
)

// Modifier bits, in HID boot-report order.
const (
	ModLeftControl uint8 = 1 << iota
	ModLeftShift
	ModLeftAlt
	ModLeftGUI
	ModRightControl
	ModRightShift
	ModRightAlt
	ModRightGUI
)
