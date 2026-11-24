//go:build darwin

package session

import (
	"errors"
	"math"
	"sync"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"

	"github.com/planckdotgg/planck/internal/apple"
	"github.com/planckdotgg/planck/pkg/capture/display"
	"github.com/planckdotgg/planck/pkg/defaults"
)

const (
	vtCodecHEVC = 0x68766331 // 'hvc1'
	vtCodecAV1  = 0x61763031 // 'av01'
)

var (
	cgInit                      sync.Once
	cgMainDisplayID             func() uint32
	cgDisplayCopyDisplayMode    func(display uint32) uintptr
	cgDisplayModeGetPixelWidth  func(mode uintptr) uint
	cgDisplayModeGetPixelHeight func(mode uintptr) uint
	cgDisplayModeGetRefreshRate func(mode uintptr) float64
	cgDisplayModeRelease        func(mode uintptr)
	vtIsHardwareDecodeSupported func(codec uint32) bool
)

func loadCG() {
	cgInit.Do(func() {
		apple.Register(&cgMainDisplayID, apple.CoreGraphics, "CGMainDisplayID")
		apple.Register(&cgDisplayCopyDisplayMode, apple.CoreGraphics, "CGDisplayCopyDisplayMode")
		apple.Register(&cgDisplayModeGetPixelWidth, apple.CoreGraphics, "CGDisplayModeGetPixelWidth")
		apple.Register(&cgDisplayModeGetPixelHeight, apple.CoreGraphics, "CGDisplayModeGetPixelHeight")
		apple.Register(&cgDisplayModeGetRefreshRate, apple.CoreGraphics, "CGDisplayModeGetRefreshRate")
		apple.Register(&cgDisplayModeRelease, apple.CoreGraphics, "CGDisplayModeRelease")
		apple.Register(&vtIsHardwareDecodeSupported, apple.VideoToolbox, "VTIsHardwareDecodeSupported")
		_, _ = purego.Dlopen("/System/Library/Frameworks/AppKit.framework/AppKit", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	})
}

// probeEncoders lists VideoToolbox, which encodes HEVC Main10 and H.264 on
// every Apple silicon GPU.
func probeEncoders(HostConfig) []defaults.EncoderCap {
	return []defaults.EncoderCap{{Adapter: appleGPU, API: defaults.APIVideoToolbox, Engines: 1, DrivesDisplay: true,
		Codecs: []defaults.CodecCap{{Codec: defaults.CodecH264}, {Codec: defaults.CodecHEVC, TenBit: true}}}}
}

func hostDisplays(cfg HostConfig) []defaults.HostDisplay {
	if cfg.Synth {
		return []defaults.HostDisplay{{ID: "synthetic", Width: cfg.Width, Height: cfg.Height, RefreshHz: max(cfg.FPS, 60), Primary: true}}
	}
	all, err := display.List()
	if err != nil {
		return nil
	}
	var out []defaults.HostDisplay
	for _, d := range all {
		out = append(out, defaults.HostDisplay{ID: d.ID, Width: d.Width, Height: d.Height, RefreshHz: int(math.Round(d.Refresh)), Primary: d.Primary})
	}
	return out
}

func vddCap() defaults.VDDCap { return defaults.VDDCap{} }

type vddManager struct{}

var errNoVDD = errors.New("session: virtual displays need SudoVDA on Windows")

func (*vddManager) create(VideoParams, []byte, bool) (VirtualDisplay, error) { return nil, errNoVDD }
func (*vddManager) close() error                                             { return nil }

// clientDisplay is the main display's current mode in backing pixels, and
// the most frames a second its screen shows (ProMotion: 120). The main
// display is the one with the menu bar, NSScreen.screens[0]; mainScreen is
// the screen of the key window, which can be another display.
func clientDisplay() defaults.ClientDisplay { return WindowDisplay(0) }

// WindowDisplay is clientDisplay for the display the NSWindow win is on,
// which a full-screen window fills; with win 0, or a window on no screen,
// it is the main display's. Call it on the main thread.
func WindowDisplay(win uintptr) defaults.ClientDisplay {
	loadCG()
	var scr objc.ID
	if win != 0 {
		scr = objc.ID(win).Send(objc.RegisterName("screen"))
	}
	return screenDisplay(scr)
}

// screenDisplay reads the NSScreen scr's display, or the main display's
// when scr is 0.
func screenDisplay(scr objc.ID) defaults.ClientDisplay {
	id := cgMainDisplayID()
	if scr != 0 {
		if n, ok := screenDisplayID(scr); ok {
			id = n
		}
	} else if cls := objc.ID(objc.GetClass("NSScreen")); cls != 0 {
		scr = firstScreen(cls)
	}
	var d defaults.ClientDisplay
	if mode := cgDisplayCopyDisplayMode(id); mode != 0 {
		d.WidthPx, d.HeightPx = int(cgDisplayModeGetPixelWidth(mode)), int(cgDisplayModeGetPixelHeight(mode))
		d.MaxHz = int(math.Round(cgDisplayModeGetRefreshRate(mode)))
		cgDisplayModeRelease(mode)
	}
	if scr != 0 {
		if hz := objc.Send[int](scr, objc.RegisterName("maximumFramesPerSecond")); hz > 0 {
			d.MaxHz = hz
		}
		d.Scale = objc.Send[float64](scr, objc.RegisterName("backingScaleFactor"))
	}
	if d.MaxHz <= 0 {
		d.MaxHz = 60
	}
	d.MinHz = min(24, d.MaxHz)
	return d
}

// screenDisplayID is the CGDirectDisplayID of an NSScreen.
func screenDisplayID(scr objc.ID) (uint32, bool) {
	desc := scr.Send(objc.RegisterName("deviceDescription"))
	if desc == 0 {
		return 0, false
	}
	key := apple.String("NSScreenNumber")
	defer apple.CFRelease(key)
	num := desc.Send(objc.RegisterName("objectForKey:"), key)
	if num == 0 {
		return 0, false
	}
	return objc.Send[uint32](num, objc.RegisterName("unsignedIntValue")), true
}

func firstScreen(cls objc.ID) objc.ID {
	screens := cls.Send(objc.RegisterName("screens"))
	if screens == 0 || objc.Send[uint](screens, objc.RegisterName("count")) == 0 {
		return 0
	}
	return screens.Send(objc.RegisterName("objectAtIndex:"), 0)
}

// clientDecoders lists VideoToolbox's hardware decoders and the Metal
// PyroWave decoder, whose output is 8-bit until it writes BGR10A2.
func clientDecoders() []defaults.CodecCap {
	loadCG()
	var out []defaults.CodecCap
	if vtIsHardwareDecodeSupported(vtCodecHEVC) {
		out = append(out, defaults.CodecCap{Codec: defaults.CodecHEVC, TenBit: true})
	}
	if vtIsHardwareDecodeSupported(vtCodecAV1) {
		out = append(out, defaults.CodecCap{Codec: defaults.CodecAV1, TenBit: true})
	}
	return append(out, defaults.CodecCap{Codec: defaults.CodecPyroWave, Chroma444: true})
}

const (
	clientPresent10Bit  = false
	clientDirectDisplay = true
)
