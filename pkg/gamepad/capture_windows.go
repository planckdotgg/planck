//go:build windows

package gamepad

import (
	"fmt"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

type xinputGamepad struct {
	Buttons        uint16
	LT, RT         uint8
	LX, LY, RX, RY int16
}

type xinputState struct {
	Packet  uint32
	Gamepad xinputGamepad
}

type xinputCapabilities struct {
	Type, SubType uint8
	Flags         uint16
	Gamepad       xinputGamepad
	LeftMotor     uint16
	RightMotor    uint16
}

type xinputBattery struct{ Type, Level uint8 }

const (
	xinputSlots           = 4
	batteryDevtypeGamepad = 0
	batteryTypeWired      = 1
	batteryTypeUnknown    = 0xFF
	batteryTypeDisconnect = 0
	xinputGuide           = 0x0400
)

// xinput is the newest XInput DLL found. getState is XInputGetStateEx
// (ordinal 100) where the DLL has it: unlike XInputGetState it reports the
// Guide button.
type xinput struct {
	getState, setState, getCaps, getBattery uintptr
}

var (
	xinputOnce sync.Once
	xinputDLL  xinput
	xinputErr  error
)

func loadXInput() (xinput, error) {
	xinputOnce.Do(func() {
		for _, name := range []string{"xinput1_4.dll", "xinput1_3.dll", "xinput9_1_0.dll"} {
			h, err := windows.LoadLibraryEx(name, 0, windows.LOAD_LIBRARY_SEARCH_SYSTEM32)
			if err != nil {
				xinputErr = err
				continue
			}
			proc := func(n string) uintptr { p, _ := windows.GetProcAddress(h, n); return p }
			x := xinput{getState: proc("XInputGetState"), setState: proc("XInputSetState"),
				getCaps: proc("XInputGetCapabilities"), getBattery: proc("XInputGetBatteryInformation")}
			if ex, err := windows.GetProcAddressByOrdinal(h, 100); err == nil {
				x.getState = ex
			}
			if x.getState == 0 || x.setState == 0 {
				xinputErr = fmt.Errorf("gamepad: %s lacks XInputGetState", name)
				continue
			}
			xinputDLL, xinputErr = x, nil
			return
		}
		xinputErr = fmt.Errorf("gamepad: no XInput DLL: %w", xinputErr)
	})
	return xinputDLL, xinputErr
}

// Capture reads XInput controllers, slots 0 to 3; a controller's ID and
// Slot are its XInput user index.
type Capture struct {
	x     xinput
	state xinputState
	mu    sync.Mutex
}

func Open() (*Capture, error) {
	x, err := loadXInput()
	if err != nil {
		return nil, err
	}
	return &Capture{x: x}, nil
}

func (c *Capture) Close() error { return nil }

// Controllers lists the connected controllers.
func (c *Capture) Controllers() []ControllerInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []ControllerInfo
	for i := range uint32(xinputSlots) {
		if c.read(i) != nil {
			continue
		}
		out = append(out, c.info(i))
	}
	return out
}

func (c *Capture) read(i uint32) error {
	r, _, _ := syscall.SyscallN(c.x.getState, uintptr(i), uintptr(unsafe.Pointer(&c.state)))
	if r != 0 {
		if r == uintptr(windows.ERROR_DEVICE_NOT_CONNECTED) {
			return ErrNotConnected
		}
		return fmt.Errorf("gamepad: XInputGetState: %w", syscall.Errno(r))
	}
	return nil
}

func (c *Capture) info(i uint32) ControllerInfo {
	info := ControllerInfo{ID: int(i), Slot: int(i), Name: "XInput Controller", GUID: XInputGUID(1),
		BatteryPct: -1, Connected: true}
	if c.x.getCaps != 0 {
		var caps xinputCapabilities
		if r, _, _ := syscall.SyscallN(c.x.getCaps, uintptr(i), 0, uintptr(unsafe.Pointer(&caps))); r == 0 {
			info.GUID = XInputGUID(caps.SubType)
		}
	}
	if c.x.getBattery != 0 {
		var b xinputBattery
		if r, _, _ := syscall.SyscallN(c.x.getBattery, uintptr(i), batteryDevtypeGamepad, uintptr(unsafe.Pointer(&b))); r == 0 {
			switch b.Type {
			case batteryTypeWired:
				info.Connection = ConnectionUSB
			case batteryTypeDisconnect, batteryTypeUnknown:
			default:
				// XInput reports four levels, empty to full.
				info.Connection, info.BatteryPct = ConnectionWireless, int(b.Level)*100/3
			}
		}
	}
	return info
}

// Read fills raw with controller id's inputs in SDL's XInput order, the
// one DefaultMapping reads.
func (c *Capture) Read(id int, raw *RawState) error {
	if id < 0 || id >= xinputSlots {
		return ErrNotConnected
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.read(uint32(id)); err != nil {
		return err
	}
	g := c.state.Gamepad
	raw.Buttons = append(raw.Buttons[:0],
		g.Buttons&uint16(ButtonA) != 0, g.Buttons&uint16(ButtonB) != 0,
		g.Buttons&uint16(ButtonX) != 0, g.Buttons&uint16(ButtonY) != 0,
		g.Buttons&uint16(ButtonLeftShoulder) != 0, g.Buttons&uint16(ButtonRightShoulder) != 0,
		g.Buttons&uint16(ButtonBack) != 0, g.Buttons&uint16(ButtonStart) != 0,
		g.Buttons&uint16(ButtonLeftThumb) != 0, g.Buttons&uint16(ButtonRightThumb) != 0,
		g.Buttons&xinputGuide != 0)
	stick := func(v int16) Axis { return Axis{Value: int32(v), Min: -32768, Max: 32767} }
	// SDL's Y axes grow downward.
	flip := func(v int16) Axis { return Axis{Value: -int32(v) - 1, Min: -32768, Max: 32767} }
	trigger := func(v uint8) Axis { return Axis{Value: int32(v), Min: 0, Max: 255} }
	raw.Axes = append(raw.Axes[:0], stick(g.LX), flip(g.LY), stick(g.RX), flip(g.RY), trigger(g.LT), trigger(g.RT))
	var hat uint8
	for _, d := range [...]struct {
		bit uint32
		hat uint8
	}{{ButtonDPadUp, HatUp}, {ButtonDPadRight, HatRight}, {ButtonDPadDown, HatDown}, {ButtonDPadLeft, HatLeft}} {
		if uint32(g.Buttons)&d.bit != 0 {
			hat |= d.hat
		}
	}
	raw.Hats = append(raw.Hats[:0], hat)
	return nil
}

// Rumble sets controller id's motors, 0..255 each.
func (c *Capture) Rumble(id int, large, small uint8) error {
	if id < 0 || id >= xinputSlots {
		return ErrNotConnected
	}
	vib := [2]uint16{uint16(large) * 257, uint16(small) * 257}
	r, _, _ := syscall.SyscallN(c.x.setState, uintptr(id), uintptr(unsafe.Pointer(&vib)))
	switch {
	case r == uintptr(windows.ERROR_DEVICE_NOT_CONNECTED):
		return ErrNotConnected
	case r != 0:
		return fmt.Errorf("gamepad: XInputSetState: %w", syscall.Errno(r))
	}
	return nil
}

// DefaultMapping is the XInput layout, which XInput controllers read raw in.
func (c *Capture) DefaultMapping(id int) Mapping {
	if id < 0 || id >= xinputSlots {
		return DefaultMapping("", "")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	info := c.info(uint32(id))
	return DefaultMapping(info.GUID, info.Name)
}
