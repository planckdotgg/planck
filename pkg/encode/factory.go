package encode

import (
	"fmt"
	"sync"

	"github.com/watzon/planck/pkg/gpu"
)

// Backend opens one vendor's hardware encoder on a device of that vendor.
// Backends register themselves when their package is imported; import
// pkg/encode/all for every backend of the platform.
type Backend struct {
	Name   string
	Vendor gpu.Vendor
	Open   func(dev gpu.Device, cfg Config) (Encoder, error)
}

var (
	backendsMu sync.RWMutex
	backends   = map[gpu.Vendor]Backend{}
)

// Register makes b the encoder for its vendor.
func Register(b Backend) {
	backendsMu.Lock()
	defer backendsMu.Unlock()
	backends[b.Vendor] = b
}

// BackendFor returns the encoder registered for vendor.
func BackendFor(vendor gpu.Vendor) (Backend, bool) {
	backendsMu.RLock()
	defer backendsMu.RUnlock()
	b, ok := backends[vendor]
	return b, ok
}

// Open opens the encoder of the GPU that holds the frames, for zero copy:
// devices[0] is the capture device (NVIDIA -> NVENC, AMD -> AMF, Intel ->
// oneVPL, Apple -> VideoToolbox). A non-zero cfg.Adapter picks the device
// in devices with that LUID instead; the caller then moves each frame to
// that device before Encode.
func Open(cfg Config, devices ...gpu.Device) (Encoder, error) {
	if len(devices) == 0 {
		return nil, fmt.Errorf("encode: no device to encode on")
	}
	dev := devices[0]
	if cfg.Adapter != 0 {
		found := false
		for _, d := range devices {
			if d.Adapter.LUID == cfg.Adapter {
				dev, found = d, true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("encode: no device given for adapter LUID 0x%x", uint64(cfg.Adapter))
		}
	}
	b, ok := BackendFor(dev.Adapter.Vendor)
	if !ok {
		return nil, fmt.Errorf("%w: no encoder backend for %s (%s)", ErrUnsupported, dev.Adapter.Vendor, dev.Adapter.Name)
	}
	enc, err := b.Open(dev, cfg)
	if err != nil {
		return nil, fmt.Errorf("encode: %s on %s: %w", b.Name, dev.Adapter.Name, err)
	}
	return enc, nil
}
