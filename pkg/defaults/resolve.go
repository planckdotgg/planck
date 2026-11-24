package defaults

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/planckdotgg/planck/pkg/gpu"
)

const (
	pyroBPP420          = 1.6  // bits per pixel, PyroWave 4:2:0 (Punktfunk default; author eval 125–300 Mbps @ 35 dB)
	pyro444Factor       = 1.18 // author eval: 4:4:4 costs 15–20%
	pyroSerialBudget    = 0.20 // max fraction of the frame interval spent putting one frame on the wire
	hevcLANBPP          = 0.20 // HEVC/AV1 target bits per pixel on LAN
	minWANBPP           = 0.05 // below this, step resolution down
	lanCeilingKbps      = 300_000
	lanLinkShare        = 0.5 // HEVC ceiling at most half the link
	lanUnknownLinkKbps  = 150_000
	tailscaleDirectKbps = 100_000
	wanKbps             = 20_000 // tailscale.RelayMaxBitrateKbps
	hevc444Factor       = 1.25
	pyroProbedShare     = 0.8
	maxAutoFPS          = 240
	lanDatagram         = 1452 // wire.MaxLANDatagram
	defaultDatagram     = 1152 // wire.MaxDatagram
)

// Seed MaxPixelRate when the probe has not run (not measured; replaced by
// the probe): NVENC Ada 2 engines 2.0e9 split / 1.0e9 unsplit, AMF VCN3
// 5.0e8, oneVPL UHD770 5.0e8, VT M-series 1.0e9.
func seedPixelRate(e EncoderCap, split bool) int64 {
	if e.MaxPixelRate > 0 {
		return e.MaxPixelRate
	}
	switch e.API {
	case APINVENC:
		if split && e.Engines >= 2 {
			return 2.0e9
		}
		return 1.0e9
	case APIVideoToolbox:
		return 1.0e9
	}
	return 5.0e8
}

var (
	ladderLines = []int{2160, 1440, 1080, 720}
	ladderFPS   = []int{240, 144, 120, 60}
)

// Resolve is pure. Precedence per knob: host.Forced > client override > auto.
// An override the hardware or route cannot honour falls back to the auto
// value with a SourceFallback Decision and a Warning. Resolve never errors:
// HEVC 4:2:0 8-bit at the capture size is always reachable.
func Resolve(host HostCapabilities, client ClientCapabilities, route RouteInfo, overrides UserOverrides) ResolvedProfile {
	r := resolver{host: host, client: client, route: route}
	r.p.Decisions = []Decision{}
	r.p.Warnings = []string{}
	r.overrides(overrides)
	r.routeInfo()
	r.encodeAdapter()
	r.virtualDisplay()
	r.sizeAndFPS()
	r.codec()
	r.depthAndChroma()
	r.bitrate()
	r.splitAndSlices()
	r.present()
	r.fec()
	return r.p
}

type resolver struct {
	host   HostCapabilities
	client ClientCapabilities
	route  RouteInfo

	user, forced, o UserOverrides

	p        ResolvedProfile
	encoders []EncoderCap
	enc      EncoderCap
	pyro     *EncoderCap
	display  *HostDisplay

	resIdx, fpsIdx int
	// stepW, stepH is the shape stepDown keeps; zero keeps the client
	// display's.
	stepW, stepH int
}

func (r *resolver) source(get func(UserOverrides) string) Source {
	if !isAuto(get(r.forced)) {
		return SourceHost
	}
	if !isAuto(get(r.user)) {
		return SourceUser
	}
	return SourceAuto
}

func (r *resolver) decide(knob, value string, src Source, why string) int {
	r.p.Decisions = append(r.p.Decisions, Decision{Knob: knob, Value: value, Source: src, Why: why})
	return len(r.p.Decisions) - 1
}

func (r *resolver) fallback(knob, asked, value, why string) int {
	r.p.Warnings = append(r.p.Warnings, fmt.Sprintf("%s %s: %s; using %s", knob, asked, why, value))
	return r.decide(knob, value, SourceFallback, why)
}

func (r *resolver) warn(format string, args ...any) {
	r.p.Warnings = append(r.p.Warnings, fmt.Sprintf(format, args...))
}

// 1. Overrides.
func (r *resolver) overrides(user UserOverrides) {
	r.user = user.Normalize()
	r.forced = r.host.Forced.Normalize()
	r.o = Merge(r.user, r.forced).Normalize()
}

// 2. Route.
func (r *resolver) routeInfo() {
	kind := r.route.Kind
	if kind != RouteLAN && kind != RouteTailscaleDirect {
		kind = RouteWAN
	}
	r.p.Route = kind
	r.p.MaxDatagram = defaultDatagram
	if kind == RouteLAN && r.route.LANDatagram {
		r.p.MaxDatagram = lanDatagram
	}
	if h, c := r.host.Link.SpeedMbps, r.client.Link.SpeedMbps; h > 0 && c > 0 {
		r.p.LinkMbps = min(h, c)
	}
	r.p.Wired = r.host.Link.Wired && r.client.Link.Wired
	switch {
	case r.o.RoutePolicy == "lan" && kind != RouteLAN:
		r.warn("routePolicy lan: the session runs over %s", kind)
	case r.o.RoutePolicy == "tailscale" && kind == RouteLAN:
		r.warn("routePolicy tailscale: the session runs over %s", kind)
	}
}

func vendorRank(v gpu.Vendor) int {
	switch v {
	case gpu.VendorNVIDIA:
		return 0
	case gpu.VendorAMD:
		return 1
	case gpu.VendorIntel:
		return 2
	case gpu.VendorApple:
		return 3
	}
	return 4
}

func (r *resolver) captureDisplay() *HostDisplay {
	ds := r.host.Displays
	if r.host.PinnedDisplay != "" {
		for i := range ds {
			if ds[i].ID == r.host.PinnedDisplay {
				return &ds[i]
			}
		}
	}
	for i := range ds {
		if ds[i].Primary {
			return &ds[i]
		}
	}
	if len(ds) > 0 {
		return &ds[0]
	}
	return nil
}

func (r *resolver) vddPossible() bool {
	return r.o.VirtualDisplay != ToggleOff && r.host.VDD.Available && r.host.PinnedDisplay == ""
}

func (r *resolver) vddOnAdapter(a gpu.Adapter) bool {
	if r.host.VDD.CanSetRenderAdapter {
		return true
	}
	g := r.host.VDD.RegistryGPU
	return g != "" && strings.Contains(strings.ToLower(a.Name), strings.ToLower(g))
}

// 3. Encode adapter.
func (r *resolver) encodeAdapter() {
	r.encoders = slices.Clone(r.host.Encoders)
	slices.SortFunc(r.encoders, func(a, b EncoderCap) int {
		return cmp.Or(
			cmp.Compare(a.Adapter.LUID, b.Adapter.LUID),
			cmp.Compare(a.API, b.API),
			cmp.Compare(a.Adapter.Name, b.Adapter.Name),
		)
	})
	var hw []EncoderCap
	for _, e := range r.encoders {
		if e.API != APIPyroWave {
			hw = append(hw, e)
		}
	}
	r.display = r.captureDisplay()

	chosen := -1
	src := r.source(func(o UserOverrides) string { return o.VirtualDisplayGPU })
	if want := r.o.VirtualDisplayGPU; !isAuto(want) {
		for i, e := range hw {
			if strings.Contains(strings.ToLower(e.Adapter.Name), strings.ToLower(want)) {
				chosen = i
				break
			}
		}
		if chosen >= 0 {
			r.decide("encodeAdapter", hw[chosen].Adapter.Name, src, "virtualDisplayGPU "+strconv.Quote(want))
		}
	}
	if chosen < 0 && len(hw) > 0 {
		var cands []int
		for i, e := range hw {
			if (r.vddPossible() && r.vddOnAdapter(e.Adapter)) || (r.display != nil && e.Adapter.LUID == r.display.Adapter) {
				cands = append(cands, i)
			}
		}
		if len(cands) == 0 {
			for i := range hw {
				cands = append(cands, i)
			}
		}
		chosen = slices.MinFunc(cands, func(a, b int) int {
			ea, eb := hw[a], hw[b]
			pa, pb := ea.EncodeP50Micros, eb.EncodeP50Micros
			if (pa == 0) != (pb == 0) {
				if pa == 0 {
					return 1
				}
				return -1
			}
			return cmp.Or(cmp.Compare(pa, pb),
				cmp.Compare(vendorRank(ea.Adapter.Vendor), vendorRank(eb.Adapter.Vendor)),
				cmp.Compare(a, b))
		})
		why := "only candidate"
		if len(cands) > 1 {
			why = "fastest encoder"
			if p := hw[chosen].EncodeP50Micros; p > 0 {
				why = fmt.Sprintf("fastest encoder: p50 %.2f ms", float64(p)/1000)
			}
		}
		if !isAuto(r.o.VirtualDisplayGPU) {
			r.fallback("encodeAdapter", strconv.Quote(r.o.VirtualDisplayGPU), hw[chosen].Adapter.Name, "no encoder on a matching GPU")
		} else {
			r.decide("encodeAdapter", hw[chosen].Adapter.Name, SourceAuto, why)
		}
	}
	if chosen >= 0 {
		r.enc = hw[chosen]
	}
	r.p.EncodeAdapter = r.enc.Adapter
	r.p.EncoderAPI = r.enc.API
	if chosen < 0 {
		return
	}
	for i, e := range r.encoders {
		if e.API == APIPyroWave && e.Adapter.LUID == r.enc.Adapter.LUID {
			r.pyro = &r.encoders[i]
			break
		}
	}
}

// 4. Virtual display.
func (r *resolver) virtualDisplay() {
	on := r.vddPossible() && r.vddOnAdapter(r.enc.Adapter)
	r.p.VirtualDisplay = on
	if !on && r.display != nil {
		r.p.CaptureDisplay = r.display.ID
	}
	src := r.source(func(o UserOverrides) string { return string(o.VirtualDisplay) })
	value := onOff(on)
	if r.o.VirtualDisplay == ToggleOn && !on {
		why := "no virtual display driver"
		switch {
		case !r.host.VDD.Available:
		case r.host.PinnedDisplay != "":
			why = "the host pins display " + r.host.PinnedDisplay
		default:
			why = "the driver cannot render on " + r.enc.Adapter.Name
		}
		r.fallback("virtualDisplay", "on", value, why)
		return
	}
	why := "renders at the client's size"
	switch {
	case src != SourceAuto:
		why = "override"
	case on:
	case r.host.PinnedDisplay != "":
		why = "the host pins display " + r.host.PinnedDisplay
	case !r.host.VDD.Available:
		why = "no virtual display driver"
	default:
		why = "the driver cannot render on " + r.enc.Adapter.Name
	}
	r.decide("virtualDisplay", value, src, why)
}

func (r *resolver) widthAt(lines int) int {
	cw, ch := r.client.Display.WidthPx, r.client.Display.HeightPx
	if cw <= 0 || ch <= 0 {
		cw, ch = 16, 9
	}
	return (lines * cw / ch) &^ 1
}

func (r *resolver) stepWidthAt(lines int) int {
	if r.stepW > 0 && r.stepH > 0 {
		return (lines * r.stepW / r.stepH) &^ 1
	}
	return r.widthAt(lines)
}

func (r *resolver) pixelRate() int64 {
	return int64(r.p.Width) * int64(r.p.Height) * int64(r.p.FPS)
}

// stepDown moves one rung down the ladder: fewer lines at the stream's
// aspect while a virtual display allows it, then a lower frame rate.
func (r *resolver) stepDown() bool {
	if r.p.VirtualDisplay {
		for _, l := range ladderLines {
			if l < r.p.Height {
				r.p.Width, r.p.Height = r.stepWidthAt(l), l
				return true
			}
		}
	}
	for _, f := range ladderFPS {
		if f < r.p.FPS {
			r.p.FPS = f
			return true
		}
	}
	return false
}

// askedSize reports whether the resolution override asks for w x h: a
// preset by its lines, a custom size exactly.
func (r *resolver) askedSize(w, h int) bool {
	switch r.o.Resolution {
	case "1080p", "1440p", "4k":
		return map[ResolutionPref]int{"1080p": 1080, "1440p": 1440, "4k": 2160}[r.o.Resolution] == h
	case "custom":
		return r.o.CustomWidth == w && r.o.CustomHeight == h
	}
	return false
}

func sizeString(w, h int) string { return fmt.Sprintf("%dx%d", w, h) }

func (r *resolver) splitPossible() bool {
	return r.p.Route == RouteLAN && r.enc.API == APINVENC && r.enc.Engines >= 2 && r.o.NVENC2EngineSplit != ToggleOff
}

// 5. Size and FPS.
func (r *resolver) sizeAndFPS() {
	maxHz := r.client.Display.MaxHz
	if maxHz <= 0 {
		maxHz = 60
	}
	resSrc := r.source(func(o UserOverrides) string { return string(o.Resolution) })
	fpsSrc := r.source(func(o UserOverrides) string { return string(o.FPS) })
	resWhy, resFallback := "", ""
	var fpsCeil int
	if r.p.VirtualDisplay {
		r.p.Width, r.p.Height = r.client.Display.WidthPx, r.client.Display.HeightPx
		if r.p.Width <= 0 || r.p.Height <= 0 {
			r.p.Width, r.p.Height = 1920, 1080
		}
		resWhy = "client native"
		if vw, vh := r.client.Display.ViewWidthPx, r.client.Display.ViewHeightPx; vw > 0 && vh > 0 && isAuto(string(r.o.Resolution)) {
			r.p.Width, r.p.Height = ViewSize(vw, vh)
			r.stepW, r.stepH = r.p.Width, r.p.Height
			resWhy = "client window"
		}
		fpsCeil = min(maxHz, maxAutoFPS)
		switch r.o.Resolution {
		case "1080p", "1440p", "4k":
			lines := map[ResolutionPref]int{"1080p": 1080, "1440p": 1440, "4k": 2160}[r.o.Resolution]
			r.p.Width, r.p.Height = r.widthAt(lines), lines
			resWhy = "override at the client aspect"
		case "custom":
			if w, h := r.o.CustomWidth, r.o.CustomHeight; validCustomSize(w, h) {
				r.p.Width, r.p.Height = w, h
				resWhy = "override"
			} else {
				resFallback = "invalid custom size " + sizeString(w, h)
			}
		}
	} else {
		r.p.Width, r.p.Height, fpsCeil = 1920, 1080, min(maxHz, 60)
		if d := r.display; d != nil {
			r.p.Width, r.p.Height = d.Width, d.Height
			if d.RefreshHz > 0 {
				fpsCeil = min(maxHz, d.RefreshHz)
			}
		}
		resWhy = "capture display size"
		if !isAuto(string(r.o.Resolution)) {
			if r.askedSize(r.p.Width, r.p.Height) {
				resWhy = "override, the capture display's size"
			} else {
				resFallback = "needs a virtual display"
			}
		}
	}

	r.p.FPS = fpsCeil
	fpsWhy, fpsFallback := "client refresh", ""
	if !r.p.VirtualDisplay {
		fpsWhy = "min of client and display refresh"
	}
	if !isAuto(string(r.o.FPS)) {
		n, _ := strconv.Atoi(string(r.o.FPS))
		if n > 0 && n <= fpsCeil {
			r.p.FPS, fpsWhy = n, "override"
		} else {
			fpsFallback = fmt.Sprintf("clamped to %d Hz", fpsCeil)
		}
	}

	rate := seedPixelRate(r.enc, r.splitPossible())
	w0, h0, f0 := r.p.Width, r.p.Height, r.p.FPS
	for r.pixelRate() > rate && r.stepDown() {
	}
	if r.p.Width != w0 || r.p.Height != h0 {
		why := fmt.Sprintf("encoder sustains %d Mpx/s", rate/1e6)
		if resSrc != SourceAuto && resFallback == "" {
			resFallback = why
		} else {
			resWhy = why
		}
	}
	if r.p.FPS != f0 {
		why := fmt.Sprintf("encoder sustains %d Mpx/s", rate/1e6)
		if fpsSrc != SourceAuto && fpsFallback == "" {
			fpsFallback = why
		} else {
			fpsWhy = why
		}
	}

	size := sizeString(r.p.Width, r.p.Height)
	if resFallback != "" {
		asked := string(r.o.Resolution)
		if r.o.Resolution == "custom" {
			asked = sizeString(r.o.CustomWidth, r.o.CustomHeight)
		}
		r.resIdx = r.fallback("resolution", asked, size, resFallback)
	} else {
		r.resIdx = r.decide("resolution", size, resSrc, resWhy)
	}
	fps := strconv.Itoa(r.p.FPS)
	if fpsFallback != "" {
		r.fpsIdx = r.fallback("fps", string(r.o.FPS), fps, fpsFallback)
	} else {
		r.fpsIdx = r.decide("fps", fps, fpsSrc, fpsWhy)
	}
}

func findCap(caps []CodecCap, c Codec, w, h int) (CodecCap, bool) {
	for _, cc := range caps {
		if cc.Codec == c && (cc.MaxWidth == 0 || w <= cc.MaxWidth) && (cc.MaxHeight == 0 || h <= cc.MaxHeight) {
			return cc, true
		}
	}
	return CodecCap{}, false
}

func (r *resolver) encCap(c Codec) (CodecCap, bool) {
	if c == CodecPyroWave {
		if r.pyro == nil {
			return CodecCap{}, false
		}
		return findCap(r.pyro.Codecs, c, r.p.Width, r.p.Height)
	}
	return findCap(r.enc.Codecs, c, r.p.Width, r.p.Height)
}

func (r *resolver) decCap(c Codec) (CodecCap, bool) {
	return findCap(r.client.Decoders, c, r.p.Width, r.p.Height)
}

func (r *resolver) common(c Codec) bool {
	_, e := r.encCap(c)
	_, d := r.decCap(c)
	return e && d
}

func (r *resolver) pyroReqKbps(chroma444 bool) int {
	bpp := pyroBPP420
	if chroma444 {
		bpp *= pyro444Factor
	}
	return int(float64(r.pixelRate()) * bpp / 1000)
}

// pyroFits is PyroWave eligibility condition 4: one whole frame must cross
// the link within pyroSerialBudget of the frame interval. It returns why
// not when it does not fit.
func (r *resolver) pyroFits(kbps int) (bool, string) {
	if r.p.LinkMbps <= 0 {
		return false, "link speed unknown"
	}
	budget := pyroSerialBudget * float64(r.p.LinkMbps) * 1000
	if float64(kbps) > budget {
		frameMs := float64(kbps) / float64(r.p.FPS) / float64(r.p.LinkMbps*1000) * 1000
		budgetMs := pyroSerialBudget * 1000 / float64(r.p.FPS)
		return false, fmt.Sprintf("serialization %.1f ms > %.1f ms budget", frameMs, budgetMs)
	}
	if r.route.ProbedMbps > 0 && float64(kbps) > pyroProbedShare*float64(r.route.ProbedMbps)*1000 {
		return false, fmt.Sprintf("needs %d Mbps > %.0f%% of probed %d Mbps", kbps/1000, pyroProbedShare*100, r.route.ProbedMbps)
	}
	return true, ""
}

func (r *resolver) pyroAutoEligible() (bool, string) {
	switch {
	case r.p.Route != RouteLAN || r.p.MaxDatagram != lanDatagram:
		return false, "PyroWave needs a LAN route with 1452-byte datagrams"
	case !r.p.Wired:
		return false, "PyroWave needs a wired link on both ends"
	case !r.common(CodecPyroWave):
		return false, "PyroWave not available on both ends"
	}
	if ok, why := r.pyroFits(r.pyroReqKbps(false)); !ok {
		return false, why
	}
	if r.pyro.MaxPixelRate <= 0 {
		return false, "PyroWave encoder rate not probed"
	}
	if r.pixelRate() > r.pyro.MaxPixelRate {
		return false, fmt.Sprintf("PyroWave encoder sustains %d Mpx/s", r.pyro.MaxPixelRate/1e6)
	}
	return true, "PyroWave fits the wired LAN"
}

// 6. Codec.
func (r *resolver) codec() {
	src := r.source(func(o UserOverrides) string { return string(o.Codec) })
	pyroOK, pyroWhy := r.pyroAutoEligible()
	auto, autoWhy := CodecHEVC, "no common codec; HEVC is always tried"
	ea, encAV1 := r.encCap(CodecAV1)
	switch {
	case pyroOK:
		auto, autoWhy = CodecPyroWave, pyroWhy
	case r.p.Route == RouteWAN && encAV1 && ea.TenBit && r.decTenBit(CodecAV1):
		auto, autoWhy = CodecAV1, "AV1 10-bit on both ends: more quality per bit on WAN"
	case r.common(CodecHEVC):
		auto, autoWhy = CodecHEVC, "HEVC on both ends"
	case r.common(CodecAV1):
		auto, autoWhy = CodecAV1, "AV1 on both ends"
	case r.common(CodecH264):
		auto, autoWhy = CodecH264, "H.264 is the only common codec"
	}
	if auto == CodecHEVC && !pyroOK && r.p.Route == RouteLAN && r.pyro != nil {
		autoWhy = "HEVC; " + pyroWhy
	}

	want := map[CodecPref]Codec{"pyrowave": CodecPyroWave, "hevc": CodecHEVC, "av1": CodecAV1, "h264": CodecH264}[r.o.Codec]
	switch {
	case want == 0:
		r.p.Codec = auto
		r.decide("codec", auto.String(), SourceAuto, autoWhy)
	case want == CodecPyroWave && r.p.Route != RouteLAN:
		r.p.Codec = auto
		r.fallback("codec", "pyrowave", auto.String(), "PyroWave runs on LAN routes only")
	case !r.common(want):
		r.p.Codec = auto
		r.fallback("codec", want.String(), auto.String(), want.String()+" not available on both ends")
	default:
		r.p.Codec = want
		r.decide("codec", want.String(), src, "override")
		if want == CodecPyroWave && !r.p.Wired {
			r.warn("codec pyrowave: the link is not wired on both ends; expect loss bursts")
		}
	}
	if r.p.Codec == CodecPyroWave {
		r.p.EncoderAPI = APIPyroWave
	}
}

func (r *resolver) decTenBit(c Codec) bool {
	d, ok := r.decCap(c)
	return ok && d.TenBit
}

// 7. Bit depth and chroma.
func (r *resolver) depthAndChroma() {
	c := r.p.Codec
	ec, _ := r.encCap(c)
	dc, _ := r.decCap(c)

	tenOK, autoDepth, depthWhy, noTen := false, 8, "", ""
	if c == CodecPyroWave {
		tenOK = r.client.Present10Bit
		noTen = "the client cannot present 10-bit"
	} else {
		tenOK = ec.TenBit && dc.TenBit
		noTen = "10-bit " + c.String() + " not on both ends"
	}
	switch {
	case tenOK && c != CodecPyroWave && r.enc.API == APIAMF:
		depthWhy = "AMF Main10 from BGRA capture is unmeasured"
	case tenOK:
		autoDepth, depthWhy = 10, "10-bit on both ends"
	default:
		depthWhy = noTen
	}
	r.p.BitDepth = autoDepth
	src := r.source(func(o UserOverrides) string { return string(o.BitDepth) })
	switch r.o.BitDepth {
	case "8":
		r.p.BitDepth = 8
		r.decide("bitDepth", "8", src, "override")
	case "10":
		if tenOK {
			r.p.BitDepth = 10
			r.decide("bitDepth", "10", src, "override")
		} else {
			r.fallback("bitDepth", "10", strconv.Itoa(autoDepth), noTen)
		}
	default:
		r.decide("bitDepth", strconv.Itoa(autoDepth), SourceAuto, depthWhy)
	}

	both444 := ec.Chroma444 && dc.Chroma444
	auto444, why := false, ""
	switch {
	case r.p.Route != RouteLAN:
		why = "4:4:4 only on LAN"
	case !both444:
		why = "4:4:4 not on both ends"
	case c == CodecPyroWave:
		auto444, why = r.pyroFits(r.pyroReqKbps(true))
		if auto444 {
			why = "4:4:4 fits the serialization budget"
		}
	default:
		ceiling := r.ceilingKbps()
		need := int(float64(r.targetKbps()) * hevc444Factor)
		auto444 = need <= ceiling
		why = fmt.Sprintf("4:4:4 needs %d Mbps, ceiling %d Mbps", need/1000, ceiling/1000)
	}
	src = r.source(func(o UserOverrides) string { return string(o.Chroma) })
	value := func(b bool) string {
		if b {
			return "444"
		}
		return "420"
	}
	switch r.o.Chroma {
	case "420":
		r.decide("chroma", "420", src, "override")
	case "444":
		if both444 {
			r.p.Chroma444 = true
			r.decide("chroma", "444", src, "override")
		} else {
			r.p.Chroma444 = auto444
			r.fallback("chroma", "444", value(auto444), "4:4:4 not on both ends")
		}
	default:
		r.p.Chroma444 = auto444
		r.decide("chroma", value(auto444), SourceAuto, why)
	}
}

func (r *resolver) ceilingKbps() int {
	switch r.p.Route {
	case RouteLAN:
		if r.p.LinkMbps <= 0 {
			return lanUnknownLinkKbps
		}
		return min(lanCeilingKbps, int(lanLinkShare*float64(r.p.LinkMbps)*1000))
	case RouteTailscaleDirect:
		return tailscaleDirectKbps
	}
	return wanKbps
}

// targetKbps is the 4:2:0 HEVC/AV1/H.264 target at the current size.
func (r *resolver) targetKbps() int {
	return int(float64(r.pixelRate()) * hevcLANBPP / 1000)
}

func mbps(kbps int) string { return fmt.Sprintf("%.1f Mbps", float64(kbps)/1000) }

// 8. Bitrate.
func (r *resolver) bitrate() {
	src := r.source(func(o UserOverrides) string {
		if o.BitrateMbps == 0 {
			return ""
		}
		return "set"
	})
	asked := fmt.Sprintf("%d Mbps", r.o.BitrateMbps)

	if r.p.Codec == CodecPyroWave {
		req := r.pyroReqKbps(r.p.Chroma444)
		r.p.BitrateKbps, r.p.MaxBitrateKbps = req, req
		switch v := r.o.BitrateMbps * 1000; {
		case v == 0:
			r.decide("bitrate", mbps(req), SourceAuto, "PyroWave CBR budget")
		case v > req:
			r.fallback("bitrate", asked, mbps(req), "above the PyroWave CBR budget")
		default:
			r.p.BitrateKbps, r.p.MaxBitrateKbps = v, v
			r.decide("bitrate", mbps(v), src, "override")
		}
		return
	}

	ceiling := r.ceilingKbps()
	if r.p.Route != RouteLAN && isAuto(string(r.o.Resolution)) {
		w0, h0, f0 := r.p.Width, r.p.Height, r.p.FPS
		bpp := func() float64 { return float64(ceiling) * 1000 / float64(r.pixelRate()) }
		low := bpp()
		for bpp() < minWANBPP && r.stepDown() {
		}
		why := fmt.Sprintf("%s ceiling: %.3f bpp < %.2f at %s@%d", mbps(ceiling), low, minWANBPP, sizeString(w0, h0), f0)
		if r.p.Width != w0 || r.p.Height != h0 {
			d := &r.p.Decisions[r.resIdx]
			d.Value, d.Why = sizeString(r.p.Width, r.p.Height), why
		}
		if r.p.FPS != f0 {
			d := &r.p.Decisions[r.fpsIdx]
			d.Value, d.Why = strconv.Itoa(r.p.FPS), why
		}
	}
	target := r.targetKbps()
	if r.p.Chroma444 {
		target = int(float64(target) * hevc444Factor)
	}
	r.p.MaxBitrateKbps = ceiling
	r.p.BitrateKbps = min(target, ceiling)
	switch v := r.o.BitrateMbps * 1000; {
	case v == 0:
		why := fmt.Sprintf("%.2f bpp target, ceiling %s", hevcLANBPP, mbps(ceiling))
		if target > ceiling {
			why = "route ceiling " + mbps(ceiling)
		}
		r.decide("bitrate", mbps(r.p.BitrateKbps), SourceAuto, why)
	case v > ceiling:
		r.p.BitrateKbps = ceiling
		r.fallback("bitrate", asked, mbps(ceiling), "route ceiling "+mbps(ceiling))
	default:
		r.p.BitrateKbps, r.p.MaxBitrateKbps = v, v
		r.decide("bitrate", mbps(v), src, "override")
	}
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// 9. Split and slices.
func (r *resolver) splitAndSlices() {
	splitSrc := r.source(func(o UserOverrides) string { return string(o.NVENC2EngineSplit) })
	slicesSrc := r.source(func(o UserOverrides) string { return string(o.SubFrameSlices) })
	if r.p.Codec == CodecPyroWave {
		if r.o.NVENC2EngineSplit == ToggleOn {
			r.fallback("split", "on", "off", "PyroWave does not use NVENC")
		} else {
			r.decide("split", "off", splitSrc, "PyroWave does not use NVENC")
		}
		if !isAuto(string(r.o.SubFrameSlices)) {
			r.fallback("slices", string(r.o.SubFrameSlices), "0", "the PyroWave packetizer slices itself")
		} else {
			r.decide("slices", "0", SourceAuto, "the PyroWave packetizer slices itself")
		}
		return
	}

	engines2 := r.enc.API == APINVENC && r.enc.Engines >= 2
	splitRule := r.p.Height >= 1440 || r.p.Height >= 1080 && r.p.FPS >= 200
	var splitDecision func()
	switch r.o.NVENC2EngineSplit {
	case ToggleOn:
		if engines2 {
			r.p.Split = true
			splitDecision = func() { r.decide("split", "on", splitSrc, "override") }
		} else {
			splitDecision = func() { r.fallback("split", "on", "off", "needs NVENC with 2 encode engines") }
		}
	case ToggleOff:
		splitDecision = func() { r.decide("split", "off", splitSrc, "override") }
	default:
		why := "split pays at >=1440 lines or >=1080 lines at >=200 fps"
		switch {
		case !engines2:
			why = "needs NVENC with 2 encode engines"
		case r.p.Route != RouteLAN:
			why = "split costs a third more bits; LAN only"
		}
		r.p.Split = engines2 && r.p.Route == RouteLAN && splitRule
		splitDecision = func() { r.decide("split", onOff(r.p.Split), SourceAuto, why) }
	}

	subFrameAPI := r.enc.API == APINVENC && r.enc.SubFrame
	slicesWhy := ""
	if isAuto(string(r.o.SubFrameSlices)) {
		r.p.Slices, slicesWhy = 1, "whole-picture encode"
		if subFrameAPI && (r.p.Codec == CodecHEVC || r.p.Codec == CodecH264) && !r.p.Split {
			r.p.Slices, r.p.SubFrame, slicesWhy = 4, true, "NVENC sub-frame slices"
		}
	} else {
		r.p.Slices, _ = strconv.Atoi(string(r.o.SubFrameSlices))
		r.p.SubFrame = r.p.Slices > 1 && r.enc.SubFrame
		slicesWhy = "override"
		if r.p.Slices > 1 && r.p.Split {
			r.p.Split = false
			if r.o.NVENC2EngineSplit == ToggleOn {
				r.warn("split on conflicts with %d slices: slices win, split off", r.p.Slices)
				splitDecision = func() { r.fallback("split", "on", "off", "conflicts with codec slices") }
			} else {
				splitDecision = func() { r.decide("split", "off", SourceAuto, "codec slices requested") }
			}
		}
	}
	splitDecision()
	r.decide("slices", strconv.Itoa(r.p.Slices), slicesSrc, slicesWhy)
}

// 10. Present.
func (r *resolver) present() {
	src := r.source(func(o UserOverrides) string { return string(o.MetalDirectDisplay) })
	r.p.DirectDisplay = r.client.DirectDisplay && r.o.MetalDirectDisplay != ToggleOff
	switch {
	case r.o.MetalDirectDisplay == ToggleOn && !r.client.DirectDisplay:
		r.fallback("directDisplay", "on", "off", "the client has no direct display path")
	case src != SourceAuto:
		r.decide("directDisplay", onOff(r.p.DirectDisplay), src, "override")
	case r.p.DirectDisplay:
		r.decide("directDisplay", "on", SourceAuto, "full screen with the drawer closed")
	default:
		r.decide("directDisplay", "off", SourceAuto, "the client has no direct display path")
	}
	r.p.VSync = r.o.VSyncMode == ToggleOn
	src = r.source(func(o UserOverrides) string { return string(o.VSyncMode) })
	why := "lowest latency"
	if src != SourceAuto {
		why = "override"
	}
	r.decide("vsync", onOff(r.p.VSync), src, why)
}

// 11. FEC.
func (r *resolver) fec() {
	switch r.p.Route {
	case RouteLAN:
		r.p.FEC = FECProfile{Adaptive: true, MinParityPercent: 0, KeyframeParityPercent: 20, CriticalParityPercent: 25}
	case RouteTailscaleDirect:
		r.p.FEC = FECProfile{Adaptive: true, MinParityPercent: 5, KeyframeParityPercent: 25, CriticalParityPercent: 30}
	default:
		r.p.FEC = FECProfile{Adaptive: true, MinParityPercent: 10, KeyframeParityPercent: 30, CriticalParityPercent: 40}
	}
}
