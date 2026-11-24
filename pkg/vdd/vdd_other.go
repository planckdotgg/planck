//go:build !windows

package vdd

import "github.com/planckdotgg/planck/pkg/gpu"

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
