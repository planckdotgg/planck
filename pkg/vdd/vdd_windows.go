//go:build windows

package vdd

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sys/windows"

	"github.com/planckdotgg/planck/pkg/capture/dxgi"
	"github.com/planckdotgg/planck/pkg/gpu"
)

// Driver is an open SudoVDA control handle and the displays made through it.
type Driver struct {
	h         windows.Handle
	pingEvery time.Duration
	errc      chan error

	mu       sync.Mutex
	closed   bool
	displays map[GUID]*VirtualDisplay
	stopPing chan struct{}
	pingDone chan struct{}
}

// Available reports whether the driver is installed and this process can open it.
func Available() bool {
	_, err := DriverInfo()
	return err == nil
}

// DriverInfo opens the device briefly. It returns ErrNotInstalled or ErrAccessDenied.
func DriverInfo() (Info, error) {
	reg := readRegistry()
	info := Info{RegistryGPUName: reg.gpuName, MaxMonitors: reg.maxMonitors}
	h, path, err := openDevice()
	info.DevicePath = path
	if err != nil {
		return info, err
	}
	defer windows.CloseHandle(h)
	if info.Protocol, err = getProtocol(h); err != nil {
		return info, err
	}
	w, err := getWatchdog(h)
	if err != nil {
		return info, err
	}
	info.Watchdog = time.Duration(w.Timeout) * time.Second
	return info, nil
}

// Open opens the device and checks the protocol (Major 0, Minor 2).
func Open() (*Driver, error) {
	h, _, err := openDevice()
	if err != nil {
		return nil, err
	}
	v, err := getProtocol(h)
	if err == nil && (v.Major != 0 || v.Minor != 2) {
		err = fmt.Errorf("%w %v", ErrProtocol, v)
	}
	var w watchdogOut
	if err == nil {
		w, err = getWatchdog(h)
	}
	if err != nil {
		windows.CloseHandle(h)
		return nil, err
	}
	every := time.Second
	if w.Timeout > 0 && time.Duration(w.Timeout)*time.Second/3 < every {
		every = time.Duration(w.Timeout) * time.Second / 3
	}
	return &Driver{h: h, pingEvery: every, errc: make(chan error, 1), displays: map[GUID]*VirtualDisplay{}}, nil
}

// Err delivers an error when the driver stops answering pings; its displays
// will then be removed by the driver's watchdog.
func (d *Driver) Err() <-chan error { return d.errc }

// SetRenderAdapter sends IOCTL_SET_RENDER_ADAPTER. It is global to the
// driver: existing SudoVDA displays move to this GPU too, and the choice
// lasts until the driver reloads.
func (d *Driver) SetRenderAdapter(luid gpu.LUID) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return ErrClosed
	}
	return setRenderAdapter(d.h, luid)
}

// CreateDisplay: SetRenderAdapter (if set) -> ADD -> wait for attach -> match
// the DXGI output -> set the mode and HDR state -> optionally make it
// primary. On any error, or a panic, it removes what it added. A second call
// with the same ID returns the same monitor; closing either value removes it.
func (d *Driver) CreateDisplay(spec DisplaySpec) (*VirtualDisplay, error) {
	spec, err := spec.normalize()
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil, ErrClosed
	}
	if spec.RenderAdapter != 0 {
		if err := setRenderAdapter(d.h, spec.RenderAdapter); err != nil {
			d.mu.Unlock()
			return nil, err
		}
	}
	out, err := add(d.h, spec)
	if err != nil {
		d.mu.Unlock()
		return nil, err
	}
	v := &VirtualDisplay{
		ID:             spec.ID,
		IddAdapter:     luidOf(out.AdapterLUID),
		TargetID:       out.TargetID,
		Width:          spec.Width,
		Height:         spec.Height,
		RefreshMilliHz: spec.RefreshMilliHz,
		drv:            d,
		spec:           spec,
	}
	d.track(v)
	d.mu.Unlock()

	done := false
	defer func() {
		if !done {
			v.Close()
		}
	}()
	if err := v.attach(spec); err != nil {
		return nil, err
	}
	done = true
	return v, nil
}

func (v *VirtualDisplay) attach(spec DisplaySpec) error {
	deadline := time.Now().Add(spec.AttachTimeout)
	var p *pathInfo
	for {
		c, err := queryActive()
		if err != nil {
			return err
		}
		if p = c.findPath(v.IddAdapter, v.TargetID); p != nil {
			if v.GDIName, err = gdiName(p); err == nil && v.GDIName != "" {
				break
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%w: target %d after %v", ErrAttachTimeout, v.TargetID, spec.AttachTimeout)
		}
		time.Sleep(attachPollInterval)
	}

	until := deadline
	if g := time.Now().Add(wrongAdapterGrace); g.After(until) {
		until = g
	}
	if err := v.matchOutput(spec, until); err != nil {
		return err
	}
	if err := setMode(v.GDIName, spec.Width, spec.Height, spec.RefreshMilliHz); err != nil {
		return err
	}
	hdr, err := setHDR(p, spec.HDR)
	if err != nil {
		return err
	}
	v.HDR = hdr
	if spec.Primary {
		return makePrimary(v.IddAdapter, v.TargetID)
	}
	return nil
}

// matchOutput finds the DXGI output named GDIName. A requested render
// adapter gets until then to take over the swap chain.
func (v *VirtualDisplay) matchOutput(spec DisplaySpec, until time.Time) error {
	for {
		outs, err := dxgi.Displays()
		if err != nil {
			return err
		}
		for _, o := range outs {
			if o.ID == v.GDIName {
				v.RenderAdapter = o.Adapter.LUID
			}
		}
		if v.RenderAdapter != 0 && (spec.RenderAdapter == 0 || v.RenderAdapter == spec.RenderAdapter) {
			return nil
		}
		if time.Now().After(until) {
			if v.RenderAdapter == 0 {
				return fmt.Errorf("%w: no DXGI output named %s", ErrAttachTimeout, v.GDIName)
			}
			return fmt.Errorf("%w: %s is on adapter 0x%x, not 0x%x", ErrWrongAdapter, v.GDIName,
				uint64(v.RenderAdapter), uint64(spec.RenderAdapter))
		}
		time.Sleep(attachPollInterval)
	}
}

// Resize gives the display a w x h mode: in place when the monitor offers
// that size, else by removing it and adding it again under the same ID,
// by which Windows keeps its settings. The display may then have another
// GDIName. On an error the display is removed.
func (v *VirtualDisplay) Resize(w, h int) error {
	spec := v.spec
	spec.Width, spec.Height = w, h
	spec, err := spec.normalize()
	if err != nil {
		return err
	}
	if w == v.Width && h == v.Height {
		return nil
	}
	// SudoVDA's monitors list the common modes beside the one they were
	// added with; any other size fails here quickly (DISP_CHANGE_BADMODE).
	if setMode(v.GDIName, w, h, spec.RefreshMilliHz) == nil {
		v.Width, v.Height, v.spec = w, h, spec
		return nil
	}
	d := v.drv
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return ErrClosed
	}
	if err := remove(d.h, v.ID); err != nil {
		d.mu.Unlock()
		return err
	}
	if spec.RenderAdapter != 0 {
		err = setRenderAdapter(d.h, spec.RenderAdapter)
	}
	var out addOut
	if err == nil {
		out, err = add(d.h, spec)
	}
	if err != nil {
		d.untrack(v.ID)
		d.mu.Unlock()
		return err
	}
	v.IddAdapter, v.TargetID, v.GDIName, v.RenderAdapter = luidOf(out.AdapterLUID), out.TargetID, "", 0
	v.Width, v.Height, v.spec = w, h, spec
	d.mu.Unlock()
	if err := v.attach(spec); err != nil {
		v.Close()
		return err
	}
	return nil
}

// Close sends REMOVE. Idempotent; the driver's STATUS_NOT_FOUND counts as success.
func (v *VirtualDisplay) Close() error {
	d := v.drv
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil
	}
	err := remove(d.h, v.ID)
	d.untrack(v.ID)
	return err
}

// Remove removes the display with this ID, whichever process added it.
func (d *Driver) Remove(id GUID) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return ErrClosed
	}
	err := remove(d.h, id)
	d.untrack(id)
	return err
}

// Close removes every display this Driver made, stops pinging, closes the handle.
func (d *Driver) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil
	}
	var errs []error
	for id := range d.displays {
		errs = append(errs, remove(d.h, id))
		d.untrack(id)
	}
	d.closed = true
	errs = append(errs, windows.CloseHandle(d.h))
	return errors.Join(errs...)
}
