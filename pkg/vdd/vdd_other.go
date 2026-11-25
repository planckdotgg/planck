//go:build !windows

package vdd

import (
	"errors"

	"github.com/planckdotgg/planck/pkg/gpu"
)

type Driver struct{}

func Available() bool                                                { return false }
func DriverInfo() (Info, error)                                      { return Info{}, ErrNotInstalled }
func Open() (*Driver, error)                                         { return nil, ErrNotInstalled }
func Monitors() ([]Monitor, error)                                   { return nil, ErrNotInstalled }
func (d *Driver) SetRenderAdapter(gpu.LUID) error                    { return ErrNotInstalled }
func (d *Driver) CreateDisplay(DisplaySpec) (*VirtualDisplay, error) { return nil, ErrNotInstalled }
func (d *Driver) Remove(GUID) error                                  { return ErrNotInstalled }
func (d *Driver) Err() <-chan error                                  { return nil }
func (d *Driver) Close() error                                       { return nil }
func (v *VirtualDisplay) Close() error                               { return nil }
func (v *VirtualDisplay) Resize(w, h int) error                      { return ErrNotInstalled }

// ErrPrivacyUnsupported is privacy mode on a system with no virtual
// display driver.
var ErrPrivacyUnsupported = errors.New("vdd: privacy mode needs a virtual display, which needs Windows")

func IsolateDisplay(string) (func() error, error) { return nil, ErrPrivacyUnsupported }
