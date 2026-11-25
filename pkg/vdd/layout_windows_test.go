//go:build windows

package vdd

import (
	"testing"
	"unsafe"
)

func TestIOCTLCodes(t *testing.T) {
	ctl := func(fn uint32) uint32 { return 0x22<<16 | fn<<2 } // FILE_DEVICE_UNKNOWN, METHOD_BUFFERED, FILE_ANY_ACCESS
	for code, fn := range map[uint32]uint32{
		ioctlAdd: 0x800, ioctlRemove: 0x801, ioctlSetRenderAdapter: 0x802,
		ioctlGetWatchdog: 0x803, ioctlPing: 0x888, ioctlProtocolVersion: 0x8FF,
	} {
		if ctl(fn) != code {
			t.Errorf("function 0x%x: code 0x%x, want 0x%x", fn, code, ctl(fn))
		}
	}
}

func TestLayouts(t *testing.T) {
	var a addParams
	var o addOut
	var p pathInfo
	var m modeInfo
	var h deviceInfoHeader
	var sn sourceDeviceName
	checks := []struct {
		name      string
		got, want uintptr
	}{
		{"addParams", unsafe.Sizeof(a), 56},
		{"addParams.RefreshRate", unsafe.Offsetof(a.RefreshRate), 8},
		{"addParams.MonitorGUID", unsafe.Offsetof(a.MonitorGUID), 12},
		{"addParams.DeviceName", unsafe.Offsetof(a.DeviceName), 28},
		{"addParams.SerialNumber", unsafe.Offsetof(a.SerialNumber), 42},
		{"addOut", unsafe.Sizeof(o), 12},
		{"addOut.TargetID", unsafe.Offsetof(o.TargetID), 8},
		{"watchdogOut", unsafe.Sizeof(watchdogOut{}), 8},
		{"protocolVersion", unsafe.Sizeof(protocolVersion{}), 4},
		{"pathInfo", unsafe.Sizeof(p), 72},
		{"pathInfo.Target", unsafe.Offsetof(p.Target), 20},
		{"pathInfo.Target.ID", unsafe.Offsetof(p.Target) + unsafe.Offsetof(p.Target.ID), 28},
		{"pathInfo.Flags", unsafe.Offsetof(p.Flags), 68},
		{"modeInfo", unsafe.Sizeof(m), 64},
		{"modeInfo.Union", unsafe.Offsetof(m.Union), 16},
		{"deviceInfoHeader", unsafe.Sizeof(h), 20},
		{"deviceInfoHeader.ID", unsafe.Offsetof(h.ID), 16},
		{"sourceDeviceName", unsafe.Sizeof(sn), 84},
		{"adapterName", unsafe.Sizeof(adapterName{}), 276},
		{"targetDeviceName", unsafe.Sizeof(targetDeviceName{}), 420},
		{"targetDeviceName.MonitorFriendlyDeviceName", unsafe.Offsetof(targetDeviceName{}.MonitorFriendlyDeviceName), 36},
		{"advancedColorInfo", unsafe.Sizeof(advancedColorInfo{}), 32},
		{"setAdvancedColorState", unsafe.Sizeof(setAdvancedColorState{}), 24},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}
}

func TestDeviceInstance(t *testing.T) {
	iface := deviceInstance(`\\?\ROOT#DISPLAY#0001#{e5bcc234-1e0c-418a-a0d4-ef8b7501414d}`)
	adapter := deviceInstance(`\\?\root#display#0001#{5b45201d-f2f2-4f3b-85bb-30ff1f953599}`)
	if iface != "root#display#0001" || adapter != iface {
		t.Fatalf("instances %q and %q", iface, adapter)
	}
}
