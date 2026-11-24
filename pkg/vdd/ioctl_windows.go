//go:build windows

package vdd

import (
	"errors"
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"github.com/planckdotgg/planck/pkg/gpu"
)

// SudoVDA protocol 0.2.1 (sudovda-ioctl.h).
const (
	ioctlAdd              = 0x222000
	ioctlRemove           = 0x222004
	ioctlSetRenderAdapter = 0x222008
	ioctlGetWatchdog      = 0x22200C
	ioctlPing             = 0x222220
	ioctlProtocolVersion  = 0x2223FC
)

var ifaceGUID = windows.GUID{Data1: 0xe5bcc234, Data2: 0x1e0c, Data3: 0x418a,
	Data4: [8]byte{0xa0, 0xd4, 0xef, 0x8b, 0x75, 0x01, 0x41, 0x4d}}

const registryKey = `SOFTWARE\SudoMaker\SudoVDA`

type addParams struct { // VIRTUAL_DISPLAY_ADD_PARAMS
	Width, Height, RefreshRate uint32
	MonitorGUID                windows.GUID
	DeviceName                 [14]byte
	SerialNumber               [14]byte
}

type addOut struct { // VIRTUAL_DISPLAY_ADD_OUT
	AdapterLUID windows.LUID
	TargetID    uint32
}

type watchdogOut struct{ Timeout, Countdown uint32 }

type protocolVersion struct{ Major, Minor, Incremental, Test uint8 }

func luidOf(l windows.LUID) gpu.LUID {
	return gpu.LUID(uint64(uint32(l.HighPart))<<32 | uint64(l.LowPart))
}

func winLUID(l gpu.LUID) windows.LUID {
	return windows.LUID{LowPart: uint32(l), HighPart: int32(uint32(l >> 32))}
}

// devicePath returns the first present SudoVDA control interface.
func devicePath() (string, error) {
	paths, err := windows.CM_Get_Device_Interface_List("", &ifaceGUID, windows.CM_GET_DEVICE_INTERFACE_LIST_PRESENT)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrNotInstalled, err)
	}
	for _, p := range paths {
		if p != "" {
			return p, nil
		}
	}
	return "", ErrNotInstalled
}

func openDevice() (windows.Handle, string, error) {
	path, err := devicePath()
	if err != nil {
		return 0, "", err
	}
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, "", err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	switch {
	case errors.Is(err, windows.ERROR_ACCESS_DENIED):
		return 0, path, fmt.Errorf("%w: %s", ErrAccessDenied, path)
	case errors.Is(err, windows.ERROR_FILE_NOT_FOUND), errors.Is(err, windows.ERROR_PATH_NOT_FOUND):
		return 0, path, fmt.Errorf("%w: %v", ErrNotInstalled, err)
	case err != nil:
		return 0, path, fmt.Errorf("vdd: open %s: %w", path, err)
	}
	return h, path, nil
}

// ioctl sends one synchronous request. in and out point at the layouts above
// or are nil; windows.DeviceIoControl converts them inside its syscall.
func ioctl(h windows.Handle, code uint32, in unsafe.Pointer, inLen uintptr, out unsafe.Pointer, outLen uintptr) error {
	var n uint32
	err := windows.DeviceIoControl(h, code, (*byte)(in), uint32(inLen), (*byte)(out), uint32(outLen), &n, nil)
	if err != nil {
		return err
	}
	if n < uint32(outLen) {
		return fmt.Errorf("vdd: ioctl 0x%x returned %d of %d bytes", code, n, outLen)
	}
	return nil
}

func getProtocol(h windows.Handle) (Version, error) {
	var v protocolVersion
	if err := ioctl(h, ioctlProtocolVersion, nil, 0, unsafe.Pointer(&v), unsafe.Sizeof(v)); err != nil {
		return Version{}, fmt.Errorf("vdd: protocol version: %w", err)
	}
	return Version{Major: v.Major, Minor: v.Minor, Incremental: v.Incremental, Test: v.Test != 0}, nil
}

func getWatchdog(h windows.Handle) (watchdogOut, error) {
	var w watchdogOut
	err := ioctl(h, ioctlGetWatchdog, nil, 0, unsafe.Pointer(&w), unsafe.Sizeof(w))
	if err != nil {
		return w, fmt.Errorf("vdd: get watchdog: %w", err)
	}
	return w, nil
}

func ping(h windows.Handle) error {
	return ioctl(h, ioctlPing, nil, 0, nil, 0)
}

func setRenderAdapter(h windows.Handle, l gpu.LUID) error {
	in := winLUID(l)
	if err := ioctl(h, ioctlSetRenderAdapter, unsafe.Pointer(&in), unsafe.Sizeof(in), nil, 0); err != nil {
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return fmt.Errorf("%w: set render adapter: %v", ErrAccessDenied, err)
		}
		return fmt.Errorf("vdd: set render adapter 0x%x: %w", uint64(l), err)
	}
	return nil
}

func add(h windows.Handle, s DisplaySpec) (addOut, error) {
	in := addParams{
		Width:       uint32(s.Width),
		Height:      uint32(s.Height),
		RefreshRate: uint32(s.RefreshMilliHz),
		MonitorGUID: windows.GUID(s.ID),
	}
	copy(in.DeviceName[:maxNameLen], s.Name)
	copy(in.SerialNumber[:maxNameLen], s.Serial)
	var out addOut
	if err := ioctl(h, ioctlAdd, unsafe.Pointer(&in), unsafe.Sizeof(in), unsafe.Pointer(&out), unsafe.Sizeof(out)); err != nil {
		return out, fmt.Errorf("vdd: add display %v: %w", s.ID, err)
	}
	return out, nil
}

// remove treats an unknown GUID (STATUS_NOT_FOUND) as already removed.
func remove(h windows.Handle, id GUID) error {
	in := windows.GUID(id)
	err := ioctl(h, ioctlRemove, unsafe.Pointer(&in), unsafe.Sizeof(in), nil, 0)
	if err != nil && !errors.Is(err, windows.ERROR_NOT_FOUND) {
		return fmt.Errorf("vdd: remove display %v: %w", id, err)
	}
	return nil
}

type registryConfig struct {
	gpuName     string
	maxMonitors int
}

func readRegistry() registryConfig {
	c := registryConfig{maxMonitors: 10}
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, registryKey, registry.QUERY_VALUE)
	if err != nil {
		return c
	}
	defer k.Close()
	if s, _, err := k.GetStringValue("gpuName"); err == nil {
		c.gpuName = strings.TrimRight(s, "\x00")
	}
	if n, _, err := k.GetIntegerValue("maxMonitors"); err == nil && n > 0 {
		c.maxMonitors = int(n)
	}
	return c
}
