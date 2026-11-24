package defaults

import (
	"bytes"
	"encoding/json"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/planckdotgg/planck/pkg/gpu"
)

var (
	rtx4080 = gpu.Adapter{LUID: 0x100, Vendor: gpu.VendorNVIDIA, Name: "NVIDIA GeForce RTX 4080"}
	rx6700  = gpu.Adapter{LUID: 0x200, Vendor: gpu.VendorAMD, Name: "AMD Radeon RX 6700 XT"}
	uhd770  = gpu.Adapter{LUID: 0x300, Vendor: gpu.VendorIntel, Name: "Intel(R) UHD Graphics 770"}
)

// bigboi: p50s are the measured 1080p120 encode times from the Phase 1B
// host-engine live test.
func bigboi() HostCapabilities {
	return HostCapabilities{
		OS: "windows",
		Encoders: []EncoderCap{
			{Adapter: rtx4080, API: APINVENC, Engines: 2, SubFrame: true, EncodeP50Micros: 1410, Codecs: []CodecCap{
				{Codec: CodecH264, Chroma444: true, MaxWidth: 4096, MaxHeight: 4096},
				{Codec: CodecHEVC, TenBit: true, Chroma444: true, MaxWidth: 8192, MaxHeight: 8192},
				{Codec: CodecAV1, TenBit: true, MaxWidth: 8192, MaxHeight: 8192},
			}},
			{Adapter: rtx4080, API: APIPyroWave, MaxPixelRate: 2e9, Codecs: []CodecCap{
				{Codec: CodecPyroWave, TenBit: true, Chroma444: true},
			}},
			{Adapter: rx6700, API: APIAMF, Engines: 1, EncodeP50Micros: 3040, DrivesDisplay: true, Codecs: []CodecCap{
				{Codec: CodecH264, MaxWidth: 4096, MaxHeight: 2160},
				{Codec: CodecHEVC, TenBit: true, MaxWidth: 7680, MaxHeight: 4320},
			}},
			{Adapter: rx6700, API: APIPyroWave, MaxPixelRate: 1e9, Codecs: []CodecCap{
				{Codec: CodecPyroWave, TenBit: true, Chroma444: true},
			}},
			{Adapter: uhd770, API: APIOneVPL, Engines: 1, EncodeP50Micros: 2310, Codecs: []CodecCap{
				{Codec: CodecH264, MaxWidth: 4096, MaxHeight: 4096},
				{Codec: CodecHEVC, TenBit: true, Chroma444: true, MaxWidth: 8192, MaxHeight: 8192},
				{Codec: CodecAV1, TenBit: true, MaxWidth: 8192, MaxHeight: 8192},
			}},
		},
		Displays: []HostDisplay{{ID: `\\.\DISPLAY1`, Width: 1920, Height: 1080, RefreshHz: 60, Adapter: rx6700.LUID, Primary: true}},
		VDD:      VDDCap{Available: true, Protocol: "0.2.1", CanSetRenderAdapter: true, RegistryGPU: "NVIDIA GeForce RTX 4080"},
		Link:     Link{Wired: true, SpeedMbps: 2500},
	}
}

// m5Pro: 14" MacBook Pro on wired 2.5GbE (en7).
func m5Pro() ClientCapabilities {
	return ClientCapabilities{
		OS:      "darwin",
		Display: ClientDisplay{WidthPx: 3024, HeightPx: 1964, Scale: 2, MaxHz: 120, MinHz: 24, HDR: true},
		Decoders: []CodecCap{
			{Codec: CodecH264, MaxWidth: 4096, MaxHeight: 2304},
			{Codec: CodecHEVC, TenBit: true, MaxWidth: 8192, MaxHeight: 4320},
			{Codec: CodecAV1, TenBit: true, MaxWidth: 8192, MaxHeight: 4320},
			{Codec: CodecPyroWave, TenBit: true, Chroma444: true},
		},
		Present10Bit:  true,
		DirectDisplay: true,
		Link:          Link{Wired: true, SpeedMbps: 2500},
	}
}

var (
	lan       = RouteInfo{Kind: RouteLAN, LANDatagram: true, RTTMicros: 400}
	tsDirect  = RouteInfo{Kind: RouteTailscaleDirect, RTTMicros: 900}
	wanRelay  = RouteInfo{Kind: RouteWAN, RTTMicros: 25000}
	fecLAN    = FECProfile{Adaptive: true, MinParityPercent: 0, KeyframeParityPercent: 20, CriticalParityPercent: 25}
	fecTSDir  = FECProfile{Adaptive: true, MinParityPercent: 5, KeyframeParityPercent: 25, CriticalParityPercent: 30}
	fecWANCfg = FECProfile{Adaptive: true, MinParityPercent: 10, KeyframeParityPercent: 30, CriticalParityPercent: 40}
)

// golden is the part of a ResolvedProfile the worked cases pin down.
type golden struct {
	Adapter        string
	API            string
	VirtualDisplay bool
	CaptureDisplay string
	Codec          Codec
	BitDepth       int
	Chroma444      bool
	W, H, FPS      int
	Bitrate, Max   int
	Split          bool
	Slices         int
	SubFrame       bool
	Datagram       int
	Direct, VSync  bool
	FEC            FECProfile
}

func summarize(p ResolvedProfile) golden {
	return golden{
		Adapter: p.EncodeAdapter.Name, API: p.EncoderAPI, VirtualDisplay: p.VirtualDisplay, CaptureDisplay: p.CaptureDisplay,
		Codec: p.Codec, BitDepth: p.BitDepth, Chroma444: p.Chroma444, W: p.Width, H: p.Height, FPS: p.FPS,
		Bitrate: p.BitrateKbps, Max: p.MaxBitrateKbps, Split: p.Split, Slices: p.Slices, SubFrame: p.SubFrame,
		Datagram: p.MaxDatagram, Direct: p.DirectDisplay, VSync: p.VSync, FEC: p.FEC,
	}
}

func withLinks(h HostCapabilities, c ClientCapabilities, hostMbps, clientMbps int) (HostCapabilities, ClientCapabilities) {
	h.Link.SpeedMbps, c.Link.SpeedMbps = hostMbps, clientMbps
	return h, c
}

func TestResolveWorkedCases(t *testing.T) {
	hevc444Mac := m5Pro()
	hevc444Mac.Decoders[1].Chroma444 = true

	noVDD := bigboi()
	noVDD.VDD = VDDCap{}

	caseA := golden{
		Adapter: rtx4080.Name, API: APINVENC, VirtualDisplay: true, Codec: CodecHEVC, BitDepth: 10,
		W: 3024, H: 1964, FPS: 120, Bitrate: 142539, Max: 300000, Split: true, Slices: 1,
		Datagram: 1452, Direct: true, FEC: fecLAN,
	}
	pyro1080 := golden{
		Adapter: rtx4080.Name, API: APIPyroWave, VirtualDisplay: true, Codec: CodecPyroWave, BitDepth: 10, Chroma444: true,
		W: 1662, H: 1080, FPS: 120, Bitrate: 406666, Max: 406666, Datagram: 1452, Direct: true, FEC: fecLAN,
	}
	h10, c10 := withLinks(bigboi(), m5Pro(), 10000, 10000)
	h1, c1 := withLinks(bigboi(), m5Pro(), 1000, 2500)
	pyroNative := pyro1080
	pyroNative.W, pyroNative.H, pyroNative.Bitrate, pyroNative.Max = 3024, 1964, 1345570, 1345570
	caseA444 := caseA
	caseA444.Chroma444, caseA444.Bitrate = true, 178173

	caseB := golden{
		Adapter: rtx4080.Name, API: APINVENC, VirtualDisplay: true, Codec: CodecHEVC, BitDepth: 10,
		W: 3024, H: 1964, FPS: 120, Bitrate: 100000, Max: 100000, Slices: 4, SubFrame: true,
		Datagram: 1152, Direct: true, FEC: fecTSDir,
	}
	caseBWAN := golden{
		Adapter: rtx4080.Name, API: APINVENC, VirtualDisplay: true, Codec: CodecAV1, BitDepth: 10,
		W: 2216, H: 1440, FPS: 120, Bitrate: 20000, Max: 20000, Slices: 1,
		Datagram: 1152, Direct: true, FEC: fecWANCfg,
	}
	caseC := golden{
		Adapter: rx6700.Name, API: APIAMF, VirtualDisplay: true, Codec: CodecHEVC, BitDepth: 8,
		W: 2216, H: 1440, FPS: 120, Bitrate: 76584, Max: 300000, Slices: 1,
		Datagram: 1452, Direct: true, FEC: fecLAN,
	}
	// 1080p60 PyroWave 4:4:4 needs 235 Mbps, inside the 500 Mbps budget,
	// and the AMD has a PyroWave entry, so the physical case is PyroWave.
	caseCNoVDD := golden{
		Adapter: rx6700.Name, API: APIPyroWave, CaptureDisplay: `\\.\DISPLAY1`, Codec: CodecPyroWave, BitDepth: 10, Chroma444: true,
		W: 1920, H: 1080, FPS: 60, Bitrate: 234897, Max: 234897,
		Datagram: 1452, Direct: true, FEC: fecLAN,
	}
	noVDDNoPyro := bigboi()
	noVDDNoPyro.VDD = VDDCap{}
	noVDDNoPyro.Encoders = slices.DeleteFunc(noVDDNoPyro.Encoders, func(e EncoderCap) bool { return e.API == APIPyroWave })
	caseCNoVDDHEVC := golden{
		Adapter: rx6700.Name, API: APIAMF, CaptureDisplay: `\\.\DISPLAY1`, Codec: CodecHEVC, BitDepth: 8,
		W: 1920, H: 1080, FPS: 60, Bitrate: 24883, Max: 300000, Slices: 1,
		Datagram: 1452, Direct: true, FEC: fecLAN,
	}

	tests := []struct {
		name     string
		host     HostCapabilities
		client   ClientCapabilities
		route    RouteInfo
		o        UserOverrides
		want     golden
		warnings int
	}{
		{"A 4080 VDD to Mac, LAN 2.5G", bigboi(), m5Pro(), lan, UserOverrides{}, caseA, 0},
		{"A with a 4:4:4 HEVC decoder", bigboi(), hevc444Mac, lan, UserOverrides{}, caseA444, 0},
		{"A + Resolution 1080p", bigboi(), m5Pro(), lan, UserOverrides{Resolution: "1080p"}, pyro1080, 0},
		{"A with a 10G link", h10, c10, lan, UserOverrides{}, pyroNative, 0},
		{"A with a 1G link", h1, c1, lan, UserOverrides{}, caseA, 0},
		{"B TailscaleDirect", bigboi(), m5Pro(), tsDirect, UserOverrides{}, caseB, 0},
		{"B TailscaleDirect, explicit pyrowave", bigboi(), m5Pro(), tsDirect, UserOverrides{Codec: "pyrowave"}, caseB, 1},
		{"B on WAN/DERP", bigboi(), m5Pro(), wanRelay, UserOverrides{}, caseBWAN, 0},
		{"C RX 6700 XT AMF via VirtualDisplayGPU", bigboi(), m5Pro(), lan, UserOverrides{VirtualDisplayGPU: "Radeon"}, caseC, 0},
		{"C without VDD", noVDD, m5Pro(), lan, UserOverrides{}, caseCNoVDD, 0},
		{"C without VDD, Resolution override falls back", noVDD, m5Pro(), lan, UserOverrides{Resolution: "1440p"}, caseCNoVDD, 1},
		{"C without VDD or PyroWave", noVDDNoPyro, m5Pro(), lan, UserOverrides{}, caseCNoVDDHEVC, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := Resolve(tt.host, tt.client, tt.route, tt.o)
			if got := summarize(p); got != tt.want {
				t.Errorf("profile\n got %+v\nwant %+v", got, tt.want)
			}
			if len(p.Warnings) != tt.warnings {
				t.Errorf("warnings = %q, want %d", p.Warnings, tt.warnings)
			}
		})
	}
}

func TestResolveFourK80AbsentPicksFastestRemainingEncoder(t *testing.T) {
	h := bigboi()
	h.Encoders = slices.DeleteFunc(h.Encoders, func(e EncoderCap) bool { return e.Adapter.LUID == rtx4080.LUID })
	p := Resolve(h, m5Pro(), lan, UserOverrides{})
	if p.EncodeAdapter.Name != uhd770.Name {
		t.Fatalf("adapter = %s, want %s (measured p50 2.31 ms < AMF 3.04 ms)", p.EncodeAdapter.Name, uhd770.Name)
	}
	h.Encoders = slices.DeleteFunc(h.Encoders, func(e EncoderCap) bool { return e.Adapter.LUID == uhd770.LUID })
	if got := summarize(Resolve(h, m5Pro(), lan, UserOverrides{})); got.API != APIAMF || got.W != 2216 || got.BitDepth != 8 {
		t.Fatalf("AMF only: %+v", got)
	}
}

func TestResolveEncoderTieBreak(t *testing.T) {
	h := bigboi()
	for i := range h.Encoders {
		h.Encoders[i].EncodeP50Micros = 0
	}
	h.Encoders[4].EncodeP50Micros = 9000
	if p := Resolve(h, m5Pro(), lan, UserOverrides{}); p.EncodeAdapter.Name != uhd770.Name {
		t.Fatalf("measured beats unmeasured: got %s", p.EncodeAdapter.Name)
	}
	h.Encoders[4].EncodeP50Micros = 0
	if p := Resolve(h, m5Pro(), lan, UserOverrides{}); p.EncodeAdapter.Name != rtx4080.Name {
		t.Fatalf("all unmeasured: got %s, want NVIDIA first", p.EncodeAdapter.Name)
	}
}

func decision(p ResolvedProfile, knob string) Decision {
	for _, d := range p.Decisions {
		if d.Knob == knob {
			return d
		}
	}
	return Decision{}
}

func TestResolveForcedBeatsUser(t *testing.T) {
	h := bigboi()
	h.Forced = UserOverrides{Codec: "h264", FPS: "60"}
	p := Resolve(h, m5Pro(), lan, UserOverrides{Codec: "hevc", FPS: "120", Chroma: "420"})
	if p.Codec != CodecH264 || p.FPS != 60 {
		t.Fatalf("codec %v fps %d, want h264 60", p.Codec, p.FPS)
	}
	for knob, want := range map[string]Source{"codec": SourceHost, "fps": SourceHost, "chroma": SourceUser, "split": SourceAuto} {
		if d := decision(p, knob); d.Source != want {
			t.Errorf("%s: %+v, want source %d", knob, d, want)
		}
	}
}

func TestResolvePyroWaveRouteRules(t *testing.T) {
	h10, c10 := withLinks(bigboi(), m5Pro(), 10000, 10000)
	for _, route := range []RouteInfo{tsDirect, wanRelay, {Kind: RouteLAN}} {
		if p := Resolve(h10, c10, route, UserOverrides{}); p.Codec == CodecPyroWave {
			t.Errorf("route %+v: auto picked PyroWave", route)
		}
	}
	for _, route := range []RouteInfo{tsDirect, wanRelay} {
		p := Resolve(h10, c10, route, UserOverrides{Codec: "pyrowave"})
		if p.Codec == CodecPyroWave || decision(p, "codec").Source != SourceFallback || len(p.Warnings) == 0 {
			t.Errorf("route %v: explicit pyrowave not rejected: codec %v %+v", route.Kind, p.Codec, p.Warnings)
		}
	}

	wifi := c10
	wifi.Link.Wired = false
	if p := Resolve(h10, wifi, lan, UserOverrides{}); p.Codec == CodecPyroWave {
		t.Error("auto picked PyroWave over Wi-Fi")
	}
	p := Resolve(h10, wifi, lan, UserOverrides{Codec: "pyrowave"})
	if p.Codec != CodecPyroWave || decision(p, "codec").Source != SourceUser || len(p.Warnings) != 1 {
		t.Errorf("explicit pyrowave on Wi-Fi LAN: codec %v warnings %q", p.Codec, p.Warnings)
	}

	probed := lan
	probed.ProbedMbps = 400
	if p := Resolve(bigboi(), m5Pro(), probed, UserOverrides{Resolution: "1080p"}); p.Codec == CodecPyroWave {
		t.Error("PyroWave picked above 80% of the probed rate")
	}
	unknown := m5Pro()
	unknown.Link.SpeedMbps = 0
	if p := Resolve(bigboi(), unknown, lan, UserOverrides{Resolution: "1080p"}); p.Codec == CodecPyroWave || p.LinkMbps != 0 {
		t.Errorf("unknown link: codec %v link %d", p.Codec, p.LinkMbps)
	}
}

func TestResolveFallbacksWarn(t *testing.T) {
	noVDD := bigboi()
	noVDD.VDD = VDDCap{}
	slowAMD := bigboi()
	slowAMD.Encoders[2].MaxPixelRate = 5e7
	noDirect := m5Pro()
	noDirect.DirectDisplay = false
	h10, c10 := withLinks(bigboi(), m5Pro(), 10000, 10000)

	tests := []struct {
		name   string
		host   HostCapabilities
		client ClientCapabilities
		route  RouteInfo
		o      UserOverrides
		knob   string
	}{
		{"no matching GPU", bigboi(), m5Pro(), lan, UserOverrides{VirtualDisplayGPU: "Arc"}, "encodeAdapter"},
		{"VDD on without driver", noVDD, m5Pro(), lan, UserOverrides{VirtualDisplay: "on"}, "virtualDisplay"},
		{"resolution without VDD", noVDD, m5Pro(), lan, UserOverrides{Resolution: "4k"}, "resolution"},
		{"invalid custom size", bigboi(), m5Pro(), lan, UserOverrides{Resolution: "custom", CustomWidth: 641, CustomHeight: 480}, "resolution"},
		{"resolution above encoder rate", bigboi(), m5Pro(), lan, UserOverrides{VirtualDisplayGPU: "Radeon", Resolution: "4k"}, "resolution"},
		{"fps above refresh", bigboi(), m5Pro(), lan, UserOverrides{FPS: "240"}, "fps"},
		{"fps above encoder rate", slowAMD, m5Pro(), lan, UserOverrides{VirtualDisplayGPU: "Radeon", FPS: "120"}, "fps"},
		{"pyrowave off LAN", bigboi(), m5Pro(), wanRelay, UserOverrides{Codec: "pyrowave"}, "codec"},
		{"av1 on AMF", bigboi(), m5Pro(), lan, UserOverrides{VirtualDisplayGPU: "Radeon", Codec: "av1"}, "codec"},
		{"10-bit H.264", bigboi(), m5Pro(), lan, UserOverrides{Codec: "h264", BitDepth: "10"}, "bitDepth"},
		{"444 without decoder", bigboi(), m5Pro(), lan, UserOverrides{Chroma: "444"}, "chroma"},
		{"bitrate above route ceiling", bigboi(), m5Pro(), tsDirect, UserOverrides{BitrateMbps: 500}, "bitrate"},
		{"bitrate above PyroWave budget", h10, c10, lan, UserOverrides{BitrateMbps: 3000}, "bitrate"},
		{"split on AMF", bigboi(), m5Pro(), lan, UserOverrides{VirtualDisplayGPU: "Radeon", NVENC2EngineSplit: "on"}, "split"},
		{"split on with slices", bigboi(), m5Pro(), lan, UserOverrides{NVENC2EngineSplit: "on", SubFrameSlices: "4"}, "split"},
		{"split on PyroWave", h10, c10, lan, UserOverrides{NVENC2EngineSplit: "on"}, "split"},
		{"slices on PyroWave", h10, c10, lan, UserOverrides{SubFrameSlices: "8"}, "slices"},
		{"direct display without support", bigboi(), noDirect, lan, UserOverrides{MetalDirectDisplay: "on"}, "directDisplay"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := Resolve(tt.host, tt.client, tt.route, tt.o)
			fallbacks := 0
			for _, d := range p.Decisions {
				if d.Source == SourceFallback {
					fallbacks++
				}
			}
			if d := decision(p, tt.knob); d.Source != SourceFallback {
				t.Fatalf("%s decision %+v, want a fallback", tt.knob, d)
			}
			if len(p.Warnings) < fallbacks || !slices.ContainsFunc(p.Warnings, func(w string) bool { return strings.HasPrefix(w, tt.knob) }) {
				t.Fatalf("warnings %q for %d fallbacks", p.Warnings, fallbacks)
			}
		})
	}
}

// A resolution the capture display already has needs no virtual display.
func TestResolveResolutionMatchingDisplayIsNoFallback(t *testing.T) {
	noVDD := bigboi()
	noVDD.VDD = VDDCap{}
	for _, o := range []UserOverrides{{Resolution: "1080p"}, {Resolution: "custom", CustomWidth: 1920, CustomHeight: 1080}} {
		p := Resolve(noVDD, m5Pro(), lan, o)
		d := decision(p, "resolution")
		if d.Source != SourceUser || d.Value != "1920x1080" || slices.ContainsFunc(p.Warnings, func(w string) bool { return strings.HasPrefix(w, "resolution") }) {
			t.Errorf("%+v: resolution %+v, warnings %q", o, d, p.Warnings)
		}
	}
}

func TestResolveSplitSlicesConflictKeepsSlices(t *testing.T) {
	p := Resolve(bigboi(), m5Pro(), lan, UserOverrides{NVENC2EngineSplit: "on", SubFrameSlices: "4"})
	if p.Split || p.Slices != 4 || !p.SubFrame {
		t.Fatalf("split %v slices %d subframe %v, want off 4 true", p.Split, p.Slices, p.SubFrame)
	}
	p = Resolve(bigboi(), m5Pro(), lan, UserOverrides{SubFrameSlices: "8"})
	if p.Split || p.Slices != 8 || len(p.Warnings) != 0 {
		t.Fatalf("auto split with 8 slices: split %v slices %d warnings %q", p.Split, p.Slices, p.Warnings)
	}
}

func TestResolveDeterministic(t *testing.T) {
	host, client := bigboi(), m5Pro()
	want, err := json.Marshal(Resolve(host, client, lan, UserOverrides{Resolution: "1080p"}))
	if err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewPCG(1, 2))
	for i := range 1000 {
		h := bigboi()
		rng.Shuffle(len(h.Encoders), func(a, b int) { h.Encoders[a], h.Encoders[b] = h.Encoders[b], h.Encoders[a] })
		got, err := json.Marshal(Resolve(h, client, lan, UserOverrides{Resolution: "1080p"}))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("run %d: order %v changed the profile:\n%s\nwant\n%s", i, h.Encoders, got, want)
		}
	}
}

func TestResolveDecisionsCoverEveryKnob(t *testing.T) {
	p := Resolve(bigboi(), m5Pro(), lan, UserOverrides{})
	var knobs []string
	for _, d := range p.Decisions {
		knobs = append(knobs, d.Knob)
	}
	want := []string{"encodeAdapter", "virtualDisplay", "resolution", "fps", "codec", "bitDepth", "chroma", "bitrate", "split", "slices", "directDisplay", "vsync"}
	if !slices.Equal(knobs, want) {
		t.Fatalf("decision order %v, want %v", knobs, want)
	}
	if d := decision(p, "codec"); !strings.Contains(d.Why, "serialization") {
		t.Errorf("codec why %q should say why PyroWave lost", d.Why)
	}
}

func windowed(w, h int) ClientCapabilities {
	c := m5Pro()
	c.Display.ViewWidthPx, c.Display.ViewHeightPx = w, h
	return c
}

// TestResolveVirtualDisplayTakesWindowSize: with Resolution auto a
// virtual display is the stream window's size, even and in bounds; a
// headless client, an override or a physical display ignore the window.
func TestResolveVirtualDisplayTakesWindowSize(t *testing.T) {
	for _, c := range []struct {
		name         string
		client       ClientCapabilities
		o            UserOverrides
		w, h         int
		vdd          bool
		resolutionBy string
	}{
		{"window", windowed(2560, 1600), UserOverrides{}, 2560, 1600, true, "client window"},
		{"odd window", windowed(2561, 1415), UserOverrides{}, 2560, 1414, true, "client window"},
		{"tiny window", windowed(300, 200), UserOverrides{}, MinCustomWidth, MinCustomHeight, true, "client window"},
		{"headless", m5Pro(), UserOverrides{}, 3024, 1964, true, "client native"},
		{"1440p override", windowed(2560, 1600), UserOverrides{Resolution: "1440p"}, 2216, 1440, true, "override at the client aspect"},
		{"custom override", windowed(2560, 1600), UserOverrides{Resolution: "custom", CustomWidth: 1920, CustomHeight: 1200}, 1920, 1200, true, "override"},
		{"physical display", windowed(2560, 1600), UserOverrides{VirtualDisplay: ToggleOff}, 1920, 1080, false, "capture display size"},
	} {
		p := Resolve(bigboi(), c.client, lan, c.o)
		if p.VirtualDisplay != c.vdd || p.Width != c.w || p.Height != c.h {
			t.Errorf("%s: virtual display %v %dx%d, want %v %dx%d", c.name, p.VirtualDisplay, p.Width, p.Height, c.vdd, c.w, c.h)
		}
		if d := decision(p, "resolution"); d.Why != c.resolutionBy {
			t.Errorf("%s: resolution decided by %q, want %q", c.name, d.Why, c.resolutionBy)
		}
	}
}

// TestResolveWindowStepsDownAtItsShape: a window too large for the route
// steps down keeping the window's shape, not the display's.
func TestResolveWindowStepsDownAtItsShape(t *testing.T) {
	p := Resolve(bigboi(), windowed(5120, 1440), wanRelay, UserOverrides{})
	if !p.VirtualDisplay || p.Height >= 1440 || p.Width != (p.Height*5120/1440)&^1 {
		t.Fatalf("32:9 window over a relay: %dx%d, want fewer lines at 32:9", p.Width, p.Height)
	}
}
