//go:build windows

package vpl

import (
	"encoding/binary"
	"errors"
	"fmt"
	"syscall"
	"unsafe"

	"github.com/planckdotgg/planck/internal/win"
	"github.com/planckdotgg/planck/pkg/capture"
	"github.com/planckdotgg/planck/pkg/encode"
	"github.com/planckdotgg/planck/pkg/gpu"
)

// Offsets and constants of oneVPL 2.x (api/vpl from intel/libvpl, measured
// with the headers compiled by MSVC x64).
const (
	sizeVideoParam  = 208
	vpAsyncDepth    = 14
	vpMFX           = 16
	vpIOPattern     = 186
	vpExtParam      = 192
	vpNumExtParam   = 200
	mfxLowPower     = 28
	mfxBRCMult      = 30
	mfxFrameInfo    = 32
	mfxCodecID      = 100
	mfxCodecProfile = 104
	mfxTargetUsage  = 110
	mfxGopPicSize   = 112
	mfxGopRefDist   = 114
	mfxIdrInterval  = 118
	mfxRateControl  = 120
	mfxInitialDelay = 122
	mfxBufferSize   = 124
	mfxTargetKbps   = 126
	mfxMaxKbps      = 128
	mfxNumSlice     = 130
	mfxNumRefFrame  = 132

	fiBitDepthLuma   = 18
	fiBitDepthChroma = 20
	fiShift          = 22
	fiFourCC         = 32
	fiWidth          = 36
	fiHeight         = 38
	fiCropW          = 44
	fiCropH          = 46
	fiFrameRateN     = 48
	fiFrameRateD     = 52
	fiPicStruct      = 62
	fiChromaFormat   = 64

	sizeBitstream = 72
	bsTimeStamp   = 32
	bsData        = 40
	bsDataOffset  = 48
	bsDataLength  = 52
	bsMaxLength   = 56
	bsFrameType   = 62

	surfInterface   = 0
	surfData        = 88
	fdTimeStamp     = 32
	sfiRelease      = 24
	sfiNativeHandle = 56

	sizeEncodeCtrl = 56
	ecFrameType    = 32

	sizeExtCO3           = 512
	co3TargetChromaPlus1 = 158
	co3TargetDepthLuma   = 160
	co3TargetDepthChroma = 162
	co3LowDelayBRC       = 166
	extCO3               = 860832835
	sizeExtCO            = 64
	coNalHrdConformance  = 34
	extCO                = 1347372099

	handleD3D11Device = 3
	implHardware      = 2
	accelD3D11        = 768
	variantU32        = 5
	variantVersion    = 0x0100

	fourccNV12    = 842094158
	fourccP010    = 808530000
	fourccRGB4    = 876758866
	fourccA2RGB10 = 808535890
	chroma420     = 1
	chroma444     = 3
	codecHEVC     = 1129727304
	profileMain   = 1
	profileMain10 = 2
	profileRExt   = 4

	rcCBR           = 1
	targetBestSpeed = 7
	ioInVideoMemory = 1
	picProgressive  = 1
	frameI          = 1
	frameRef        = 64
	frameIDR        = 128
	codingOptionOn  = 16
	codingOptionOff = 32

	errMoreData   = -10
	wrnDeviceBusy = 2
	syncTimeoutMs = 2000
)

var (
	procCreateConfig        = dll.NewProc("MFXCreateConfig")
	procSetFilterProperty   = dll.NewProc("MFXSetConfigFilterProperty")
	procSetHandle           = dll.NewProc("MFXVideoCORE_SetHandle")
	procSyncOperation       = dll.NewProc("MFXVideoCORE_SyncOperation")
	procEncodeInit          = dll.NewProc("MFXVideoENCODE_Init")
	procEncodeReset         = dll.NewProc("MFXVideoENCODE_Reset")
	procEncodeClose         = dll.NewProc("MFXVideoENCODE_Close")
	procEncodeGetParam      = dll.NewProc("MFXVideoENCODE_GetVideoParam")
	procEncodeFrameAsync    = dll.NewProc("MFXVideoENCODE_EncodeFrameAsync")
	procGetSurfaceForEncode = dll.NewProc("MFXMemory_GetSurfaceForEncode")
)

// Status is a failed oneVPL call.
type Status struct {
	Call string
	Code int32
}

func (s *Status) Error() string { return fmt.Sprintf("vpl: %s: mfxStatus %d", s.Call, s.Code) }

func status(call string, r uintptr) error {
	if c := int32(r); c < 0 {
		return &Status{Call: call, Code: c}
	}
	return nil
}

func put16(b []byte, o int, v uint16) { binary.LittleEndian.PutUint16(b[o:], v) }
func put32(b []byte, o int, v uint32) { binary.LittleEndian.PutUint32(b[o:], v) }
func put64(b []byte, o int, v uint64) { binary.LittleEndian.PutUint64(b[o:], v) }
func addr(b []byte) uintptr           { return uintptr(unsafe.Pointer(&b[0])) }

var iidMultithread = win.NewGUID(0x9b7e4e00, 0x342c, 0x4106, [8]byte{0xa1, 0x9f, 0x4f, 0x27, 0x04, 0xf6, 0x89, 0xf0})

// Encoder is a oneVPL (Quick Sync) HEVC encoder on an Intel GPU's D3D11
// device. Each frame is copied on the GPU into a surface of the runtime's
// pool, then encoded: the runtime on this driver (API 2.8) cannot import an
// external texture (that needs API 2.10).
type Encoder struct {
	loader, session uintptr
	device          win.Device
	ctx             *win.Object
	adapter         gpu.Adapter
	cfg             encode.Config
	frame           int64

	par, co, co3, vsi []byte
	extList           []uintptr
	bs                []byte
	bsData            []byte
	ctrl              []byte
	surf, sync        *uintptr
	native            *uintptr
	resType           *uint32
	box               *win.Box
}

func setFilter(loader uintptr, name string, v uint32) error {
	r, _, _ := procCreateConfig.Call(loader)
	if r == 0 {
		return errors.New("vpl: MFXCreateConfig failed")
	}
	n, _ := syscall.BytePtrFromString(name)
	vr := make([]byte, 16)
	put16(vr, 0, variantVersion)
	put32(vr, 4, variantU32)
	put32(vr, 8, v)
	s, _, _ := procSetFilterProperty.Call(r, uintptr(unsafe.Pointer(n)), addr(vr))
	return status("MFXSetConfigFilterProperty "+name, s)
}

func sourceFourCC(f capture.Format) (uint32, error) {
	switch f {
	case capture.FormatNV12:
		return fourccNV12, nil
	case capture.FormatP010:
		return fourccP010, nil
	case capture.FormatBGRA8:
		return fourccRGB4, nil
	case capture.FormatRGB10A2:
		return fourccA2RGB10, nil
	}
	return 0, fmt.Errorf("%w: oneVPL input %s", encode.ErrUnsupported, f)
}

func align(v, a int) int { return (v + a - 1) / a * a }

// Open starts a low-latency HEVC encoder on device, an Intel adapter's
// D3D11 device. Frames passed to Encode must be textures of that device.
func Open(device win.Device, adapter gpu.Adapter, cfg encode.Config) (*Encoder, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if cfg.Codec != encode.HEVC {
		return nil, fmt.Errorf("%w: Planck drives oneVPL HEVC only", encode.ErrUnsupported)
	}
	if cfg.IntraRefreshPeriod > 0 || cfg.SubFrame {
		return nil, fmt.Errorf("%w: oneVPL intra refresh and sub-frame output", encode.ErrUnsupported)
	}
	if adapter.Vendor != gpu.VendorIntel {
		return nil, fmt.Errorf("%w: oneVPL needs an Intel adapter, not %s", encode.ErrUnsupported, adapter.Name)
	}
	fourcc, err := sourceFourCC(cfg.Source)
	if err != nil {
		return nil, err
	}
	if err := procLoad.Find(); err != nil {
		return nil, fmt.Errorf("vpl: %w", err)
	}
	loader, _, _ := procLoad.Call()
	if loader == 0 {
		return nil, errors.New("vpl: MFXLoad returned no loader")
	}
	e := &Encoder{loader: loader, device: device, adapter: adapter, cfg: cfg,
		par: make([]byte, sizeVideoParam), co: make([]byte, sizeExtCO), co3: make([]byte, sizeExtCO3), extList: make([]uintptr, 0, 3),
		bs: make([]byte, sizeBitstream), ctrl: make([]byte, sizeEncodeCtrl),
		surf: new(uintptr), sync: new(uintptr), native: new(uintptr), resType: new(uint32),
		box: &win.Box{Right: uint32(cfg.Width), Bottom: uint32(cfg.Height), Back: 1}}
	if err := e.open(fourcc); err != nil {
		e.Close()
		return nil, err
	}
	return e, nil
}

func (e *Encoder) open(fourcc uint32) error {
	for _, f := range []struct {
		name string
		v    uint32
	}{
		{"mfxImplDescription.Impl", implHardware},
		{"mfxImplDescription.AccelerationMode", accelD3D11},
		{"mfxImplDescription.VendorID", uint32(gpu.VendorIntel)},
	} {
		if err := setFilter(e.loader, f.name, f.v); err != nil {
			return err
		}
	}
	r, _, _ := procCreateSession.Call(e.loader, 0, uintptr(unsafe.Pointer(&e.session)))
	if err := status("MFXCreateSession", r); err != nil {
		return err
	}
	// The runtime calls into the device from its own threads.
	if mt, err := e.device.QueryInterface(&iidMultithread); err == nil {
		vcall(mt, 5, 1, 0, 0) // SetMultithreadProtected(TRUE)
		mt.Release()
	}
	r, _, _ = procSetHandle.Call(e.session, handleD3D11Device, uintptr(unsafe.Pointer(e.device.Object)))
	if err := status("MFXVideoCORE_SetHandle", r); err != nil {
		return err
	}
	ctx := new(*win.Object)
	vcall(e.device.Object, 40, uintptr(unsafe.Pointer(ctx)), 0, 0) // GetImmediateContext
	e.ctx = *ctx

	cfg := e.cfg
	p := e.par
	put16(p, vpAsyncDepth, 1)
	put16(p, vpIOPattern, ioInVideoMemory)
	m := p[vpMFX:]
	put32(m, mfxCodecID, codecHEVC)
	put16(m, mfxTargetUsage, targetBestSpeed)
	put16(m, mfxGopPicSize, 0xffff)
	put16(m, mfxGopRefDist, 1)
	put16(m, mfxIdrInterval, 0)
	put16(m, mfxNumRefFrame, 1)
	put16(m, mfxRateControl, rcCBR)
	put16(m, mfxNumSlice, uint16(max(cfg.Slices, 1)))
	e.putBitrate(cfg.BitrateKbps)
	fi := m[mfxFrameInfo:]
	put32(fi, fiFourCC, fourcc)
	put16(fi, fiWidth, uint16(align(cfg.Width, 32)))
	put16(fi, fiHeight, uint16(align(cfg.Height, 32)))
	put16(fi, fiCropW, uint16(cfg.Width))
	put16(fi, fiCropH, uint16(cfg.Height))
	put32(fi, fiFrameRateN, uint32(cfg.FPS))
	put32(fi, fiFrameRateD, 1)
	put16(fi, fiPicStruct, picProgressive)
	chroma, profile, depth := uint16(chroma420), uint16(profileMain), uint16(8)
	if cfg.Chroma.TenBit() {
		profile, depth = profileMain10, 10
	}
	if cfg.Chroma.Full() {
		chroma, profile = chroma444, profileRExt
	}
	put16(m, mfxCodecProfile, profile)
	put32(e.co3, 0, extCO3)
	put32(e.co3, 4, sizeExtCO3)
	// The low-power (VDEnc) engine: about half the latency of the shader
	// (VME) path on the UHD 770, low-delay rate control, and the only one
	// that takes RGB input.
	put16(m, mfxLowPower, codingOptionOn)
	if fourcc == fourccRGB4 || fourcc == fourccA2RGB10 {
		// RGB comes in as 4:4:4 and is converted to the target chroma on
		// the GPU.
		put16(fi, fiChromaFormat, chroma444)
		put16(e.co3, co3TargetChromaPlus1, chroma+1)
		put16(e.co3, co3TargetDepthLuma, depth)
		put16(e.co3, co3TargetDepthChroma, depth)
		put16(fi, fiBitDepthLuma, 8)
		put16(fi, fiBitDepthChroma, 8)
		if fourcc == fourccA2RGB10 {
			put16(fi, fiBitDepthLuma, 10)
			put16(fi, fiBitDepthChroma, 10)
		}
	} else {
		put16(fi, fiChromaFormat, chroma)
		put16(fi, fiBitDepthLuma, depth)
		put16(fi, fiBitDepthChroma, depth)
	}
	if fourcc == fourccP010 {
		put16(fi, fiShift, 1)
	}
	// Low-delay rate control sizes each frame to the channel; without HRD
	// conformance, Reset can change the rate without a new sequence.
	put16(e.co3, co3LowDelayBRC, codingOptionOn)
	put32(e.co, 0, extCO)
	put32(e.co, 4, sizeExtCO)
	put16(e.co, coNalHrdConformance, codingOptionOff)
	e.extList = append(e.extList[:0], addr(e.co3), addr(e.co))
	if cfg.HDR10 {
		e.vsi = videoSignalInfo(encode.HDR10Color)
		e.extList = append(e.extList, addr(e.vsi))
	}
	put64(p, vpExtParam, uint64(uintptr(unsafe.Pointer(&e.extList[0]))))
	put16(p, vpNumExtParam, uint16(len(e.extList)))

	r, _, _ = procEncodeInit.Call(e.session, addr(p))
	if err := status("MFXVideoENCODE_Init", r); err != nil {
		return fmt.Errorf("%w (HEVC %s from %s on %s)", err, cfg.Chroma, cfg.Source, e.adapter.Name)
	}
	// Keep the parameters the runtime settled on (it raises the buffer
	// size to its minimum), so Reset changes nothing but the rate.
	r, _, _ = procEncodeGetParam.Call(e.session, addr(p))
	if err := status("MFXVideoENCODE_GetVideoParam", r); err != nil {
		return err
	}
	n := max(cfg.Width*cfg.Height*4, 1<<20)
	e.bsData = make([]byte, n)
	put64(e.bs, bsData, uint64(addr(e.bsData)))
	put32(e.bs, bsMaxLength, uint32(n))
	put16(e.ctrl, ecFrameType, frameI|frameIDR|frameRef)
	return nil
}

// putBitrate writes a CBR rate with a one-frame buffer. Values above 65535
// kbit/s go through BRCParamMultiplier.
func (e *Encoder) putBitrate(kbps int) {
	m := e.par[vpMFX:]
	mult := kbps/0xffff + 1
	frameKB := max(kbps/8/e.cfg.FPS, 1)
	put16(m, mfxBRCMult, uint16(mult))
	put16(m, mfxTargetKbps, uint16(kbps/mult))
	put16(m, mfxMaxKbps, uint16(kbps/mult))
	put16(m, mfxBufferSize, uint16(frameKB/mult+1))
	put16(m, mfxInitialDelay, uint16(frameKB/mult/2+1))
}

func vcall(o *win.Object, i int, a, b, c uintptr) uintptr {
	vt := *(**[512]uintptr)(unsafe.Pointer(o))
	r, _, _ := syscall.SyscallN(vt[i], uintptr(unsafe.Pointer(o)), a, b, c)
	return r
}

// fnCall calls a C function pointer read from a oneVPL interface table.
func fnCall(fn, a, b, c uintptr) uintptr {
	r, _, _ := syscall.SyscallN(fn, a, b, c)
	return r
}

const ctxCopySubresourceRegion = 46

func (e *Encoder) Caps() encode.Caps {
	return encode.Caps{Adapter: e.adapter, Codecs: []encode.Codec{encode.HEVC},
		Chroma: map[encode.Codec][]encode.Chroma{encode.HEVC: {encode.NV12, encode.P010, encode.YUV444, encode.YUV444P10}}}
}

func (e *Encoder) Encode(f capture.Frame, forceKey bool, dst []byte) (encode.Packet, error) {
	r, _, _ := syscall.SyscallN(procGetSurfaceForEncode.Addr(), e.session, uintptr(unsafe.Pointer(e.surf)))
	if err := status("MFXMemory_GetSurfaceForEncode", r); err != nil {
		return encode.Packet{}, err
	}
	surf := *e.surf
	iface := *(*uintptr)(win.Ptr(surf + surfInterface))
	release := *(*uintptr)(win.Ptr(iface + sfiRelease))
	native := *(*uintptr)(win.Ptr(iface + sfiNativeHandle))
	if err := status("GetNativeHandle", fnCall(native, surf, uintptr(unsafe.Pointer(e.native)), uintptr(unsafe.Pointer(e.resType)))); err != nil {
		fnCall(release, surf, 0, 0)
		return encode.Packet{}, err
	}
	syscall.SyscallN(vtbl(e.ctx)[ctxCopySubresourceRegion], uintptr(unsafe.Pointer(e.ctx)),
		*e.native, 0, 0, 0, 0, f.Surface, 0, uintptr(unsafe.Pointer(e.box)))
	*(*uint64)(win.Ptr(surf + surfData + fdTimeStamp)) = uint64(e.frame)
	e.frame++
	var ctrl uintptr
	if forceKey {
		ctrl = addr(e.ctrl)
	}
	put32(e.bs, bsDataOffset, 0)
	put32(e.bs, bsDataLength, 0)
	for {
		r, _, _ = syscall.SyscallN(procEncodeFrameAsync.Addr(), e.session, ctrl, surf, addr(e.bs), uintptr(unsafe.Pointer(e.sync)))
		if int32(r) != wrnDeviceBusy {
			break
		}
	}
	fnCall(release, surf, 0, 0)
	if int32(r) == errMoreData {
		return encode.Packet{}, encode.ErrDropped
	}
	if err := status("MFXVideoENCODE_EncodeFrameAsync", r); err != nil {
		return encode.Packet{}, err
	}
	r, _, _ = syscall.SyscallN(procSyncOperation.Addr(), e.session, *e.sync, syncTimeoutMs)
	if err := status("MFXVideoCORE_SyncOperation", r); err != nil {
		return encode.Packet{}, err
	}
	off := binary.LittleEndian.Uint32(e.bs[bsDataOffset:])
	n := binary.LittleEndian.Uint32(e.bs[bsDataLength:])
	ft := binary.LittleEndian.Uint16(e.bs[bsFrameType:])
	return encode.Packet{
		Data:     append(dst[:0], e.bsData[off:off+n]...),
		Keyframe: ft&(frameI|frameIDR) != 0,
		PTS:      int64(binary.LittleEndian.Uint64(e.bs[bsTimeStamp:])),
	}, nil
}

func vtbl(o *win.Object) *[512]uintptr { return *(**[512]uintptr)(unsafe.Pointer(o)) }

func (e *Encoder) SetBitrate(kbps int) error {
	if kbps <= 0 {
		return fmt.Errorf("vpl: bitrate %d must be positive", kbps)
	}
	m := e.par[vpMFX:]
	mult := int(binary.LittleEndian.Uint16(m[mfxBRCMult:]))
	if kbps/max(mult, 1) > 0xffff {
		return fmt.Errorf("%w: bitrate %d kbit/s above %d set at Open", encode.ErrUnsupported, kbps, 0xffff*max(mult, 1))
	}
	put16(m, mfxTargetKbps, uint16(kbps/max(mult, 1)))
	put16(m, mfxMaxKbps, uint16(kbps/max(mult, 1)))
	r, _, _ := procEncodeReset.Call(e.session, addr(e.par))
	if err := status("MFXVideoENCODE_Reset", r); err != nil {
		return err
	}
	e.cfg.BitrateKbps = kbps
	return nil
}

func (e *Encoder) Close() error {
	if e.session != 0 {
		procEncodeClose.Call(e.session)
		procClose.Call(e.session)
		e.session = 0
	}
	if e.ctx != nil {
		e.ctx.Release()
		e.ctx = nil
	}
	if e.loader != 0 {
		procUnload.Call(e.loader)
		e.loader = 0
	}
	return nil
}

var _ encode.Encoder = (*Encoder)(nil)
