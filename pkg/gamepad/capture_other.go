//go:build !darwin && !windows && !linux

package gamepad

// Capture finds no controllers on this platform.
type Capture struct{}

func Open() (*Capture, error) { return &Capture{}, nil }

func (c *Capture) Close() error { return nil }

func (c *Capture) Controllers() []ControllerInfo { return nil }

func (c *Capture) Read(int, *RawState) error { return ErrNotConnected }

func (c *Capture) Rumble(int, uint8, uint8) error { return ErrNotConnected }

func (c *Capture) DefaultMapping(int) Mapping { return DefaultMapping("", "") }
