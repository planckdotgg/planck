package gamepad

import (
	"errors"
	"fmt"
	"os"

	"github.com/planckdotgg/planck/internal/linux/uinput"
)

// VirtualPadStatus reports whether /dev/uinput lets this user create
// virtual controllers.
func VirtualPadStatus() PadDriverStatus {
	st := PadDriverStatus{DriverName: "uinput", SupportedTypes: []PadType{PadXbox360}}
	if err := uinput.Available(); err != nil {
		st.InstallHint = uinputHint(err)
		return st
	}
	st.Installed = true
	return st
}

func uinputHint(err error) string {
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "Load the uinput kernel module (sudo modprobe uinput, and uinput in /etc/modules-load.d) to emulate gamepads."
	case errors.Is(err, os.ErrPermission):
		return "Allow your user to open /dev/uinput: install packaging/linux/60-planck-uinput.rules into /etc/udev/rules.d and log in again."
	}
	return "Gamepad emulation needs /dev/uinput: " + err.Error()
}

// OpenVirtualPads makes controllers of uinput devices. Whatever type is
// asked for, they appear as Xbox 360 controllers.
func OpenVirtualPads(t PadType) (*VirtualPads, error) {
	if err := uinput.Available(); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrUnsupported, uinputHint(err))
	}
	return newVirtualPads(&uinputBus{}, t), nil
}

type uinputBus struct{ plugs int }

type uinputPad struct{ dev *uinput.Device }

func (b *uinputBus) plug(_ PadType, onRumble func(large, small uint8)) (padTarget, error) {
	dev, err := uinput.Create(virtualPadConfig(b.plugs%MaxVirtualPads), func(strong, weak uint16) {
		large, small := rumbleLevels(strong, weak)
		onRumble(large, small)
	})
	if err != nil {
		return nil, err
	}
	b.plugs++
	return &uinputPad{dev: dev}, nil
}

func (b *uinputBus) close() error { return nil }

func (p *uinputPad) submit(s State) error { return p.dev.Emit(padEvents(s)...) }

func (p *uinputPad) unplug() error { return p.dev.Close() }

var _ padBus = (*uinputBus)(nil)
