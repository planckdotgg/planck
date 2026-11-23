//go:build windows

// Package amf drives AMD's Advanced Media Framework encoder through
// amfrt64.dll's C vtables, with no cgo and no AMF SDK at build time.
package amf

import (
	"errors"
	"fmt"
	"syscall"
	"unsafe"

	"github.com/watzon/planck/internal/win"
	"github.com/watzon/planck/pkg/capture"
	"github.com/watzon/planck/pkg/encode"
	"github.com/watzon/planck/pkg/gpu"
)

// Vtable slots from amf/public/include (AMF 1.5.3). On Windows MSVC reverses
// overloaded virtuals, which only affects the PropertyStorageEx block.
const (
	factoryCreateContext   = 0
	factoryCreateComponent = 1

	ifRelease        = 1
	ifQueryInterface = 2
	psSetProperty    = 3
	psGetProperty    = 4

	ctxTerminate                   = 13
	ctxInitDX11                    = 18
	ctxCreateSurfaceFromDX11Native = 49

	compInit        = 17
	compTerminate   = 19
	compSubmitInput = 22
	compQueryOutput = 23
	compGetCaps     = 26

	capsGetAccelerationType = 13
	capsGetInputCaps        = 14
	ioGetWidthRange         = 3
	ioGetHeightRange        = 4
	ioGetNumOfFormats       = 6
	ioGetFormatAt           = 7

	dataSetPts = 19
	dataGetPts = 20

	bufGetSize   = 24
	bufGetNative = 25
)

const (
	resultOK        = 0
	resultInputFull = 25

	dx11_0 = 110

	variantBool  = 1
	variantInt64 = 2
	variantSize  = 5
	variantRate  = 7

	surfaceNV12        = 1
	surfaceBGRA        = 3
	surfaceP010        = 10
	surfaceR10G10B10A2 = 13
)

var iidAMFBuffer = win.NewGUID(0xb04b7248, 0xb6f0, 0x4321, [8]byte{0xb6, 0x91, 0xba, 0xa4, 0x74, 0x0f, 0x9f, 0xcb})

var (
	amfDLL       = syscall.NewLazyDLL("amfrt64.dll")
	procInit     = amfDLL.NewProc("AMFInit")
	procQueryVer = amfDLL.NewProc("AMFQueryVersion")
)

type iface struct{ vtbl *[64]uintptr }

func (o *iface) call(i int, args ...uintptr) uintptr {
	all := append([]uintptr{uintptr(unsafe.Pointer(o))}, args...)
	r, _, _ := syscall.SyscallN(o.vtbl[i], all...)
	return r
}

// call3 is call for the per-frame path: a fixed argument count keeps it
// free of allocations. Pointers passed must be to the heap.
func (o *iface) call3(i int, a, b, c uintptr) uintptr {
	r, _, _ := syscall.SyscallN(o.vtbl[i], uintptr(unsafe.Pointer(o)), a, b, c)
	return r
}

func (o *iface) release() {
	if o != nil {
		o.call3(ifRelease, 0, 0, 0)
	}
}

func check(what string, r uintptr) error {
	if int32(r) != resultOK {
		return fmt.Errorf("amf: %s: AMF_RESULT %d", what, int32(r))
	}
	return nil
}

type variant struct {
	kind  int32
	_     int32
	value [16]byte
}

func int64Var(v int64) *variant {
	x := &variant{kind: variantInt64}
	*(*int64)(unsafe.Pointer(&x.value[0])) = v
	return x
}

func boolVar(v bool) *variant {
	x := &variant{kind: variantBool}
	if v {
		x.value[0] = 1
	}
	return x
}

func pairVar(kind int32, a, b uint32) *variant {
	x := &variant{kind: kind}
	*(*[2]uint32)(unsafe.Pointer(&x.value[0])) = [2]uint32{a, b}
	return x
}

func wstr(s string) *uint16 {
	p, _ := syscall.UTF16PtrFromString(s)
	return p
}

func setProp(o *iface, name string, v *variant) error {
	return check("SetProperty "+name, o.call(psSetProperty, uintptr(unsafe.Pointer(wstr(name))), uintptr(unsafe.Pointer(v))))
}

func getInt(o *iface, name string) (int64, bool) {
	var v variant
	if o.call(psGetProperty, uintptr(unsafe.Pointer(wstr(name))), uintptr(unsafe.Pointer(&v))) != resultOK {
		return 0, false
	}
	switch v.kind {
	case variantInt64:
		return *(*int64)(unsafe.Pointer(&v.value[0])), true
	case variantBool:
		return int64(v.value[0]), true
	}
	return 0, false
}

// Version reports the runtime version of amfrt64.dll as major.minor.release.build.
func Version() (string, error) {
	if err := procQueryVer.Find(); err != nil {
		return "", err
	}
	var v uint64
	if err := check("AMFQueryVersion", func() uintptr { r, _, _ := procQueryVer.Call(uintptr(unsafe.Pointer(&v))); return r }()); err != nil {
		return "", err
	}
	return fmt.Sprintf("%d.%d.%d.%d", v>>48, v>>32&0xffff, v>>16&0xffff, v&0xffff), nil
}

type session struct {
	factory *iface
	ctx     *iface
}

func openContext(device win.Device) (*session, error) {
	if err := procInit.Find(); err != nil {
		return nil, err
	}
	var ver uint64
	procQueryVer.Call(uintptr(unsafe.Pointer(&ver)))
	s := &session{}
	r, _, _ := procInit.Call(uintptr(ver), uintptr(unsafe.Pointer(&s.factory)))
	if err := check("AMFInit", r); err != nil {
		return nil, err
	}
	if err := check("CreateContext", s.factory.call(factoryCreateContext, uintptr(unsafe.Pointer(&s.ctx)))); err != nil {
		return nil, err
	}
	if err := check("InitDX11", s.ctx.call(ctxInitDX11, uintptr(unsafe.Pointer(device.Object)), dx11_0)); err != nil {
		s.ctx.release()
		return nil, err
	}
	return s, nil
}

func componentName(c encode.Codec) (string, string, error) {
	switch c {
	case encode.HEVC:
		return "AMFVideoEncoderHW_HEVC", "Hevc", nil
	case encode.AV1:
		return "AMFVideoEncoderHW_AV1", "Av1", nil
	case encode.H264:
		return "AMFVideoEncoderVCE_AVC", "", nil
	}
	return "", "", encode.ErrUnsupported
}

func (s *session) component(c encode.Codec) (*iface, error) {
	name, _, err := componentName(c)
	if err != nil {
		return nil, err
	}
	var comp *iface
	if err := check("CreateComponent "+name, s.factory.call(factoryCreateComponent, uintptr(unsafe.Pointer(s.ctx)),
		uintptr(unsafe.Pointer(wstr(name))), uintptr(unsafe.Pointer(&comp)))); err != nil {
		return nil, err
	}
	return comp, nil
}

func (s *session) close() {
	if s.ctx != nil {
		s.ctx.call(ctxTerminate)
		s.ctx.release()
	}
}

// CodecCaps is what one AMF encoder component reports.
type CodecCaps struct {
	Codec           encode.Codec
	Acceleration    int
	MaxBitrate      int64
	MaxProfile      int64
	Streams         int64
	HWInstances     int64
	MaxThroughputMB int64
	SliceOutput     bool
	Width, Height   [2]int32
	InputFormats    []int32
	Err             error
}

// Probe opens AMF on device and reports each encoder component's caps.
func Probe(device win.Device) ([]CodecCaps, error) {
	s, err := openContext(device)
	if err != nil {
		return nil, err
	}
	defer s.close()
	var out []CodecCaps
	for _, c := range []encode.Codec{encode.H264, encode.HEVC, encode.AV1} {
		cc := CodecCaps{Codec: c}
		comp, err := s.component(c)
		if err != nil {
			cc.Err = err
			out = append(out, cc)
			continue
		}
		var caps *iface
		if err := check("GetCaps", comp.call(compGetCaps, uintptr(unsafe.Pointer(&caps)))); err != nil {
			cc.Err = err
		} else {
			_, prefix, _ := componentName(c)
			cc.Acceleration = int(caps.call(capsGetAccelerationType))
			if prefix != "" {
				cc.MaxBitrate, _ = getInt(caps, prefix+"MaxBitrate")
				cc.MaxProfile, _ = getInt(caps, prefix+"MaxProfile")
				cc.Streams, _ = getInt(caps, prefix+"NumOfStreams")
				cc.HWInstances, _ = getInt(caps, prefix+"NumOfHwInstances")
				cc.MaxThroughputMB, _ = getInt(caps, prefix+"MaxThroughput")
				if c == encode.AV1 {
					cc.HWInstances, _ = getInt(caps, "Av1CapNumOfHwInstances")
					cc.MaxThroughputMB, _ = getInt(caps, "Av1CapMaxThroughput")
				}
				v, _ := getInt(caps, prefix+"SupportSliceOutput")
				cc.SliceOutput = v != 0
			}
			var io *iface
			if caps.call(capsGetInputCaps, uintptr(unsafe.Pointer(&io))) == resultOK {
				io.call(ioGetWidthRange, uintptr(unsafe.Pointer(&cc.Width[0])), uintptr(unsafe.Pointer(&cc.Width[1])))
				io.call(ioGetHeightRange, uintptr(unsafe.Pointer(&cc.Height[0])), uintptr(unsafe.Pointer(&cc.Height[1])))
				n := int(int32(io.call(ioGetNumOfFormats)))
				for i := 0; i < n; i++ {
					var f int32
					var native uint8
					if io.call(ioGetFormatAt, uintptr(i), uintptr(unsafe.Pointer(&f)), uintptr(unsafe.Pointer(&native))) == resultOK {
						cc.InputFormats = append(cc.InputFormats, f)
					}
				}
				io.release()
			}
			caps.release()
		}
		comp.release()
		out = append(out, cc)
	}
	return out, nil
}

// Encoder is an AMF HEVC encoder fed D3D11 textures of its own device.
type Encoder struct {
	s       *session
	comp    *iface
	cfg     encode.Config
	adapter gpu.Adapter
	frame   int64

	// Out parameters and property arguments of the per-frame calls, on the
	// heap so the calls do not allocate.
	surface, data, buffer *iface
	forceIDR, outType     *variant
	nameForce, nameType   *uint16
	pts                   *int64
}

// AMF_VIDEO_ENCODER_HEVC_PICTURE_TYPE_IDR and the output data types.
const (
	pictureIDR    = 2
	outputDataIDR = 0
	outputDataI   = 1
)

func surfaceFormat(f capture.Format) (int, error) {
	switch f {
	case capture.FormatBGRA8:
		return surfaceBGRA, nil
	case capture.FormatNV12:
		return surfaceNV12, nil
	case capture.FormatP010:
		return surfaceP010, nil
	case capture.FormatRGB10A2:
		return surfaceR10G10B10A2, nil
	}
	return 0, encode.ErrUnsupported
}

func Open(device win.Device, adapter gpu.Adapter, cfg encode.Config) (*Encoder, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if cfg.Codec != encode.HEVC {
		return nil, fmt.Errorf("%w: Planck drives AMF HEVC only", encode.ErrUnsupported)
	}
	if cfg.Chroma.Full() {
		return nil, fmt.Errorf("%w: AMF has no 4:4:4 encode", encode.ErrUnsupported)
	}
	if cfg.IntraRefreshPeriod > 0 || cfg.SubFrame {
		return nil, fmt.Errorf("%w: AMF intra refresh and sub-frame output", encode.ErrUnsupported)
	}
	format, err := surfaceFormat(cfg.Source)
	if err != nil {
		return nil, err
	}
	s, err := openContext(device)
	if err != nil {
		return nil, err
	}
	comp, err := s.component(cfg.Codec)
	if err != nil {
		s.close()
		return nil, err
	}
	e := &Encoder{s: s, comp: comp, cfg: cfg, adapter: adapter,
		forceIDR: int64Var(pictureIDR), outType: new(variant),
		nameForce: wstr("HevcForcePictureType"), nameType: wstr("HevcOutputDataType"), pts: new(int64)}
	bitrate := int64(cfg.BitrateKbps) * 1000
	profile, depth := int64(1), int64(8)
	if cfg.Chroma.TenBit() {
		profile, depth = 2, 10
	}
	props := []struct {
		name string
		v    *variant
	}{
		{"HevcUsage", int64Var(1)}, // ultra low latency
		{"HevcFrameSize", pairVar(variantSize, uint32(cfg.Width), uint32(cfg.Height))},
		{"HevcFrameRate", pairVar(variantRate, uint32(cfg.FPS), 1)},
		{"HevcProfile", int64Var(profile)},
		{"HevcColorBitDepth", int64Var(depth)},
		{"HevcRateControlMethod", int64Var(3)}, // CBR
		{"HevcTargetBitrate", int64Var(bitrate)},
		{"HevcPeakBitrate", int64Var(bitrate)},
		{"HevcVBVBufferSize", int64Var(bitrate / int64(cfg.FPS))},
		{"HevcGOPSize", int64Var(0)},
		{"HevcGOPSPerIDR", int64Var(0)},
		{"HevcHeaderInsertionMode", int64Var(2)}, // IDR aligned
		{"HevcMaxNumRefFrames", int64Var(1)},
		{"HevcQueryTimeout", int64Var(50)},
		{"HevcInputQueueSize", int64Var(1)},
		{"LowLatencyInternal", boolVar(true)},
		{"HevcQualityPreset", int64Var(10)}, // speed
	}
	if cfg.Slices > 1 {
		props = append(props, struct {
			name string
			v    *variant
		}{"HevcSlicesPerFrame", int64Var(int64(cfg.Slices))})
	}
	for _, p := range props {
		if err := setProp(comp, p.name, p.v); err != nil {
			e.Close()
			return nil, err
		}
	}
	if err := check("Init", comp.call(compInit, uintptr(format), uintptr(cfg.Width), uintptr(cfg.Height))); err != nil {
		e.Close()
		return nil, err
	}
	return e, nil
}

func (e *Encoder) Caps() encode.Caps {
	return encode.Caps{Adapter: e.adapter, Codecs: []encode.Codec{encode.HEVC},
		Chroma: map[encode.Codec][]encode.Chroma{encode.HEVC: {encode.NV12, encode.P010}}}
}

var errNoOutput = errors.New("amf: no output within the query timeout")

func (e *Encoder) Encode(f capture.Frame, forceKey bool, dst []byte) (encode.Packet, error) {
	if err := check("CreateSurfaceFromDX11Native", e.s.ctx.call3(ctxCreateSurfaceFromDX11Native, f.Surface,
		uintptr(unsafe.Pointer(&e.surface)), 0)); err != nil {
		return encode.Packet{}, err
	}
	e.surface.call3(dataSetPts, uintptr(e.frame), 0, 0)
	e.frame++
	if forceKey {
		if err := check("SetProperty HevcForcePictureType", e.surface.call3(psSetProperty,
			uintptr(unsafe.Pointer(e.nameForce)), uintptr(unsafe.Pointer(e.forceIDR)), 0)); err != nil {
			e.surface.release()
			e.surface = nil
			return encode.Packet{}, err
		}
	}
	r := e.comp.call3(compSubmitInput, uintptr(unsafe.Pointer(e.surface)), 0, 0)
	e.surface.release()
	e.surface = nil
	if err := check("SubmitInput", r); err != nil {
		return encode.Packet{}, err
	}
	for tries := 0; ; tries++ {
		e.comp.call3(compQueryOutput, uintptr(unsafe.Pointer(&e.data)), 0, 0)
		if e.data != nil {
			break
		}
		if tries > 40 {
			return encode.Packet{}, errNoOutput
		}
	}
	defer func() {
		e.data.release()
		e.data = nil
	}()
	if err := check("QueryInterface AMFBuffer", e.data.call3(ifQueryInterface, uintptr(unsafe.Pointer(&iidAMFBuffer)),
		uintptr(unsafe.Pointer(&e.buffer)), 0)); err != nil {
		return encode.Packet{}, err
	}
	size := int(e.buffer.call3(bufGetSize, 0, 0, 0))
	ptr := e.buffer.call3(bufGetNative, 0, 0, 0)
	pkt := encode.Packet{Data: append(dst[:0], unsafe.Slice((*byte)(win.Ptr(ptr)), size)...), PTS: int64(e.data.call3(dataGetPts, 0, 0, 0))}
	if e.data.call3(psGetProperty, uintptr(unsafe.Pointer(e.nameType)), uintptr(unsafe.Pointer(e.outType)), 0) == resultOK {
		t := *(*int64)(unsafe.Pointer(&e.outType.value[0]))
		pkt.Keyframe = t == outputDataIDR || t == outputDataI
	}
	e.buffer.release()
	e.buffer = nil
	return pkt, nil
}

func (e *Encoder) SetBitrate(kbps int) error {
	if kbps <= 0 {
		return fmt.Errorf("amf: bitrate %d must be positive", kbps)
	}
	b := int64(kbps) * 1000
	for _, p := range []string{"HevcTargetBitrate", "HevcPeakBitrate", "HevcVBVBufferSize"} {
		v := b
		if p == "HevcVBVBufferSize" {
			v = b / int64(e.cfg.FPS)
		}
		if err := setProp(e.comp, p, int64Var(v)); err != nil {
			return err
		}
	}
	e.cfg.BitrateKbps = kbps
	return nil
}

func (e *Encoder) Close() error {
	if e.comp != nil {
		e.comp.call(compTerminate)
		e.comp.release()
		e.comp = nil
	}
	if e.s != nil {
		e.s.close()
		e.s = nil
	}
	return nil
}

var _ encode.Encoder = (*Encoder)(nil)
