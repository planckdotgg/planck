//go:build windows

package dxgi

import (
	"errors"
	"fmt"
	"strings"
	"syscall"
	"unsafe"

	"github.com/watzon/planck/internal/win"
	"github.com/watzon/planck/pkg/capture"
	"github.com/watzon/planck/pkg/gpu"
)

// Device is a D3D11 device on one adapter. Capture, encode, decode and
// presentation share it to keep frames on that GPU.
type Device struct {
	Info    gpu.Adapter
	Adapter win.Adapter
	D3D     win.Device
	Context win.Context
}

func (d *Device) Close() {
	if d == nil || d.D3D.Object == nil {
		return
	}
	d.Context.Release()
	d.D3D.Release()
	d.Adapter.Release()
	d.D3D.Object = nil
}

// Adapters lists the hardware adapters in DXGI order: the first is the
// system's default. An adapter can appear under more than one LUID (one per
// display path); each is listed.
func Adapters() ([]gpu.Adapter, error) {
	var out []gpu.Adapter
	err := eachAdapter(func(a win.Adapter, info gpu.Adapter) bool {
		if !info.Software {
			out = append(out, info)
		}
		return true
	})
	return out, err
}

// FindAdapter returns the first hardware adapter whose name contains match,
// ignoring case ("4080", "Radeon", "UHD").
func FindAdapter(match string) (gpu.Adapter, error) {
	all, err := Adapters()
	if err != nil {
		return gpu.Adapter{}, err
	}
	for _, a := range all {
		if strings.Contains(strings.ToLower(a.Name), strings.ToLower(match)) {
			return a, nil
		}
	}
	return gpu.Adapter{}, fmt.Errorf("dxgi: no hardware adapter matches %q", match)
}

// eachAdapter calls fn with every adapter, released after fn returns unless
// fn keeps it by returning false (which also stops the walk).
func eachAdapter(fn func(win.Adapter, gpu.Adapter) bool) error {
	f, err := win.CreateFactory()
	if err != nil {
		return fmt.Errorf("dxgi: CreateDXGIFactory1: %w", err)
	}
	defer f.Release()
	for i := 0; ; i++ {
		a, err := f.EnumAdapters1(i)
		if errors.Is(err, win.DXGI_ERROR_NOT_FOUND) {
			return nil
		}
		if err != nil {
			return err
		}
		info, err := a.Info()
		if err != nil {
			a.Release()
			return err
		}
		if !fn(a, info) {
			return nil
		}
		a.Release()
	}
}

// OpenDevice makes a hardware D3D11 device (BGRA and video support) on the
// adapter with this LUID.
func OpenDevice(luid gpu.LUID) (*Device, error) {
	var d *Device
	var openErr error
	err := eachAdapter(func(a win.Adapter, info gpu.Adapter) bool {
		if info.LUID != luid {
			return true
		}
		dev, ctx, err := win.CreateDevice(a)
		if err != nil {
			openErr = fmt.Errorf("dxgi: D3D11CreateDevice on %s: %w", info.Name, err)
			return true
		}
		d = &Device{Info: info, Adapter: a, D3D: dev, Context: ctx}
		return false
	})
	if err != nil {
		return nil, err
	}
	if openErr != nil {
		return nil, openErr
	}
	if d == nil {
		return nil, fmt.Errorf("dxgi: no adapter with LUID 0x%x", uint64(luid))
	}
	return d, nil
}

// Displays lists the outputs attached to the desktop. ID is the GDI device
// name (`\\.\DISPLAY1`), stable while the display layout does not change.
func Displays() ([]capture.Display, error) {
	var out []capture.Display
	var walkErr error
	err := eachAdapter(func(a win.Adapter, info gpu.Adapter) bool {
		outs, err := a.Outputs()
		if err != nil {
			walkErr = err
			return true
		}
		for _, o := range outs {
			if d, ok := describe(o, info); ok {
				out = append(out, d)
			}
			o.Release()
		}
		return true
	})
	if err == nil {
		err = walkErr
	}
	return out, err
}

func describe(o win.Output, info gpu.Adapter) (capture.Display, bool) {
	desc, err := o.Desc()
	if err != nil || desc.AttachedToDesktop == 0 {
		return capture.Display{}, false
	}
	r := desc.DesktopCoordinates
	d := capture.Display{
		ID:      desc.Name(),
		Name:    desc.Name(),
		Adapter: info,
		Width:   int(r.Right - r.Left),
		Height:  int(r.Bottom - r.Top),
		Primary: r.Left == 0 && r.Top == 0,
	}
	if d1, err := o.Desc1(); err == nil {
		d.HDR = d1.ColorSpace == colorSpaceHDR10
	}
	d.Refresh = refreshOf(desc.Name())
	return d, true
}

// DXGI_COLOR_SPACE_RGB_FULL_G2084_NONE_P2020: the desktop is in HDR10.
const colorSpaceHDR10 = 12

var (
	user32                  = syscall.NewLazyDLL("user32.dll")
	procEnumDisplaySettings = user32.NewProc("EnumDisplaySettingsW")
)

// refreshOf reads the display's current refresh rate from GDI; DXGI only
// reports it once duplication starts.
func refreshOf(device string) float64 {
	var dm [220]byte // DEVMODEW
	*(*uint16)(unsafe.Pointer(&dm[68])) = uint16(len(dm))
	name, _ := syscall.UTF16PtrFromString(device)
	const currentSettings = ^uintptr(0) // ENUM_CURRENT_SETTINGS
	if r, _, _ := procEnumDisplaySettings.Call(uintptr(unsafe.Pointer(name)), currentSettings, uintptr(unsafe.Pointer(&dm[0]))); r == 0 {
		return 0
	}
	return float64(*(*uint32)(unsafe.Pointer(&dm[184]))) // dmDisplayFrequency
}

// findOutput returns the output named id ("" for the primary display) and
// the adapter that drives it.
func findOutput(id string) (win.Output, gpu.Adapter, error) {
	var found win.Output
	var adapter gpu.Adapter
	err := eachAdapter(func(a win.Adapter, info gpu.Adapter) bool {
		outs, err := a.Outputs()
		if err != nil {
			return true
		}
		for _, o := range outs {
			d, ok := describe(o, info)
			if found.Object == nil && ok && (d.ID == id || id == "" && d.Primary) {
				found, adapter = o, info
				continue
			}
			o.Release()
		}
		return true
	})
	if err != nil {
		if found.Object != nil {
			found.Release()
		}
		return win.Output{}, gpu.Adapter{}, err
	}
	if found.Object == nil {
		if id == "" {
			return win.Output{}, gpu.Adapter{}, errors.New("dxgi: no primary display")
		}
		return win.Output{}, gpu.Adapter{}, fmt.Errorf("dxgi: no display %q", id)
	}
	return found, adapter, nil
}

// vtbl calls method i of a COM object with three arguments, without the
// allocation win.Object's variadic call makes. Pointers passed must be to
// the heap.
func vcall(o *win.Object, i int, a, b, c uintptr) uintptr {
	vt := *(**[512]uintptr)(unsafe.Pointer(o))
	r, _, _ := syscall.SyscallN(vt[i], uintptr(unsafe.Pointer(o)), a, b, c)
	return r
}

func addRef(o *win.Object) { vcall(o, 1, 0, 0, 0) }
