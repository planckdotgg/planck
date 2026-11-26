// Package gpu names graphics adapters across platforms so that capture and
// encode backends can agree on which device owns a frame.
package gpu

import "fmt"

type Vendor uint32

// PCI vendor IDs, plus Apple's ID for Apple silicon.
const (
	VendorUnknown Vendor = 0
	VendorNVIDIA  Vendor = 0x10de
	VendorAMD     Vendor = 0x1002
	VendorIntel   Vendor = 0x8086
	VendorApple   Vendor = 0x106b
)

func (v Vendor) String() string {
	switch v {
	case VendorNVIDIA:
		return "NVIDIA"
	case VendorAMD:
		return "AMD"
	case VendorIntel:
		return "Intel"
	case VendorApple:
		return "Apple"
	}
	return fmt.Sprintf("vendor(0x%04x)", uint32(v))
}

// LUID identifies an adapter for the lifetime of a boot on Windows (DXGI
// adapter LUID). On macOS it is the Metal registry ID; on Linux the device
// number of its DRM render node (see RenderNodes).
type LUID uint64

type Adapter struct {
	LUID      LUID
	Vendor    Vendor
	DeviceID  uint32
	Name      string
	VRAMBytes uint64
	Software  bool
}

// Device is a GPU device that frames live on, shared by capture, encode,
// decode and presentation so a frame never leaves its GPU.
type Device struct {
	Adapter Adapter
	// Handle is the ID3D11Device* on Windows. It is 0 on macOS, where
	// IOSurfaces move between Metal, VideoToolbox and ScreenCaptureKit on
	// their own, and on Linux, where frames are DMA-BUFs or memory and
	// each encoder opens its own context on the adapter.
	Handle uintptr
}
