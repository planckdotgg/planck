//go:build darwin || windows

package audio

// NewOutputPlayer plays on the output device with this Device ID, or with
// NewPlayer on the default one when id is empty, names the default, or
// names a device that is gone.
func NewOutputPlayer(id string) (Player, error) {
	if id != "" {
		devs, err := OutputDevices()
		if dev, gone := PickDevice(devs, id); err == nil && !gone && !dev.IsDefault {
			return openOutput(dev)
		}
	}
	return NewPlayer(PlayerConfig{})
}

// presentDevice is id when list has a device with it, else "": the
// default device.
func presentDevice(list func() ([]Device, error), id string) string {
	if id == "" {
		return ""
	}
	devs, err := list()
	if _, gone := PickDevice(devs, id); err != nil || gone {
		return ""
	}
	return id
}
