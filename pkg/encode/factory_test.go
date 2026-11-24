package encode

import (
	"errors"
	"testing"

	"github.com/planckdotgg/planck/pkg/capture"
	"github.com/planckdotgg/planck/pkg/gpu"
)

type fake struct{ dev gpu.Device }

func (f *fake) Caps() Caps                                         { return Caps{Adapter: f.dev.Adapter} }
func (f *fake) Encode(capture.Frame, bool, []byte) (Packet, error) { return Packet{}, nil }
func (f *fake) SetBitrate(int) error                               { return nil }
func (f *fake) Close() error                                       { return nil }

func TestOpenPicksTheFramesGPU(t *testing.T) {
	backendsMu.Lock()
	saved := backends
	backends = map[gpu.Vendor]Backend{}
	backendsMu.Unlock()
	t.Cleanup(func() { backendsMu.Lock(); backends = saved; backendsMu.Unlock() })

	for _, v := range []gpu.Vendor{gpu.VendorNVIDIA, gpu.VendorAMD} {
		Register(Backend{Name: v.String(), Vendor: v, Open: func(dev gpu.Device, cfg Config) (Encoder, error) {
			if err := cfg.Validate(); err != nil {
				return nil, err
			}
			return &fake{dev}, nil
		}})
	}
	amd := gpu.Device{Adapter: gpu.Adapter{LUID: 1, Vendor: gpu.VendorAMD, Name: "RX"}, Handle: 10}
	nv := gpu.Device{Adapter: gpu.Adapter{LUID: 2, Vendor: gpu.VendorNVIDIA, Name: "RTX"}, Handle: 20}
	intel := gpu.Device{Adapter: gpu.Adapter{LUID: 3, Vendor: gpu.VendorIntel, Name: "UHD"}, Handle: 30}
	cfg := Config{Codec: HEVC, Chroma: NV12, Source: capture.FormatBGRA8, Width: 1920, Height: 1080, FPS: 60, BitrateKbps: 20000}

	enc, err := Open(cfg, amd, nv)
	if err != nil || enc.(*fake).dev != amd {
		t.Fatalf("default: got %v, %v; want the capture device's AMD encoder", enc, err)
	}
	over := cfg
	over.Adapter = nv.Adapter.LUID
	if enc, err = Open(over, amd, nv); err != nil || enc.(*fake).dev != nv {
		t.Fatalf("override: got %v, %v; want NVIDIA", enc, err)
	}
	over.Adapter = 99
	if _, err = Open(over, amd, nv); err == nil {
		t.Fatal("override to an adapter with no device succeeded")
	}
	if _, err = Open(cfg, intel); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("no backend: got %v, want ErrUnsupported", err)
	}
	if _, err = Open(cfg); err == nil {
		t.Fatal("no device succeeded")
	}
	bad := cfg
	bad.FPS = 0
	if enc, err = Open(bad, amd); err == nil || enc != nil {
		t.Fatalf("invalid config: got %v, %v", enc, err)
	}
}

func TestOpenPrefersCodecBackend(t *testing.T) {
	backendsMu.Lock()
	savedB, savedC := backends, codecs
	backends, codecs = map[gpu.Vendor]Backend{}, map[Codec]CodecBackend{}
	backendsMu.Unlock()
	t.Cleanup(func() { backendsMu.Lock(); backends, codecs = savedB, savedC; backendsMu.Unlock() })

	nv := gpu.Device{Adapter: gpu.Adapter{LUID: 2, Vendor: gpu.VendorNVIDIA, Name: "RTX"}, Handle: 20}
	Register(Backend{Name: "vendor", Vendor: gpu.VendorNVIDIA, Open: func(dev gpu.Device, cfg Config) (Encoder, error) {
		return &fake{gpu.Device{}}, nil
	}})
	RegisterCodec(CodecBackend{Name: "PyroWave", Codec: PyroWave, Open: func(dev gpu.Device, cfg Config) (Encoder, error) {
		if err := cfg.Validate(); err != nil {
			return nil, err
		}
		return &fake{dev}, nil
	}})
	cfg := Config{Codec: PyroWave, Chroma: NV12, Source: capture.FormatBGRA8, Width: 1920, Height: 1080, FPS: 120, BitrateKbps: 400000, ChunkLen: 1391}
	enc, err := Open(cfg, nv)
	if err != nil || enc.(*fake).dev != nv {
		t.Fatalf("PyroWave: got %v, %v; want the codec backend on the RTX", enc, err)
	}
	hevc := cfg
	hevc.Codec = HEVC
	if enc, err = Open(hevc, nv); err != nil || enc.(*fake).dev == nv {
		t.Fatalf("HEVC: got %v, %v; want the vendor backend", enc, err)
	}
}

func TestValidatePyroWave(t *testing.T) {
	ok := Config{Codec: PyroWave, Chroma: YUV444P10, Source: capture.FormatRGB10A2, Width: 2560, Height: 1440, FPS: 120, BitrateKbps: 700000, ChunkLen: 1391}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mod := range map[string]func(*Config){
		"slices":      func(c *Config) { c.Slices = 4 },
		"sub-frame":   func(c *Config) { c.Slices, c.SubFrame = 2, true },
		"chunk":       func(c *Config) { c.ChunkLen = 100 },
		"nv12 source": func(c *Config) { c.Source = capture.FormatNV12 },
	} {
		c := ok
		mod(&c)
		if c.Validate() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if PyroWave.String() != "PyroWave" {
		t.Errorf("String() = %q", PyroWave.String())
	}
}
