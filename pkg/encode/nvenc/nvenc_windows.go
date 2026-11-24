//go:build windows

package nvenc

import (
	"encoding/binary"
	"errors"
	"fmt"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/planckdotgg/planck/internal/clock"
	"github.com/planckdotgg/planck/internal/win"
	"github.com/planckdotgg/planck/pkg/capture"
	"github.com/planckdotgg/planck/pkg/encode"
	"github.com/planckdotgg/planck/pkg/gpu"
)

var (
	guidHEVC       = win.NewGUID(0x790cdc88, 0x4522, 0x4d7b, [8]byte{0x94, 0x25, 0xbd, 0xa9, 0x97, 0x5f, 0x76, 0x03})
	guidAV1        = win.NewGUID(0x0a352289, 0x0aa7, 0x4759, [8]byte{0x86, 0x2d, 0x5d, 0x15, 0xcd, 0x16, 0xd2, 0x54})
	guidH264       = win.NewGUID(0x6bc82762, 0x4e63, 0x4ca4, [8]byte{0xaa, 0x85, 0x1e, 0x50, 0xf3, 0x21, 0xf6, 0xbf})
	guidHEVCMain   = win.NewGUID(0xb514c39a, 0xb55b, 0x40fa, [8]byte{0x87, 0x8f, 0xf1, 0x25, 0x3b, 0x4d, 0xfd, 0xec})
	guidHEVCMain10 = win.NewGUID(0xfa4d2b6c, 0x3a5b, 0x411a, [8]byte{0x80, 0x18, 0x0a, 0x3f, 0x5e, 0x3c, 0x9b, 0xe5})
	guidHEVCFRExt  = win.NewGUID(0x51ec32b5, 0x1b4c, 0x453c, [8]byte{0x9c, 0xbd, 0xb6, 0x16, 0xbd, 0x62, 0x13, 0x41})
	guidAV1Main    = win.NewGUID(0x5f2a39f5, 0xf14e, 0x4f95, [8]byte{0x9a, 0x9e, 0xb7, 0x6d, 0x56, 0x8f, 0xcf, 0x97})
	guidPresetP1   = win.NewGUID(0xfc0a8d3e, 0x45f8, 0x4cf8, [8]byte{0x80, 0xc7, 0x29, 0x88, 0x71, 0x59, 0x0e, 0xbf})
)

var nvencDLL = syscall.NewLazyDLL("nvEncodeAPI64.dll")

type api struct {
	table [sizeFunctionList]byte
}

func (a *api) fn(offset int) uintptr {
	return uintptr(binary.LittleEndian.Uint64(a.table[offset:]))
}

var loaded *api

func loadAPI() (*api, error) {
	if loaded != nil {
		return loaded, nil
	}
	if err := nvencDLL.Load(); err != nil {
		return nil, fmt.Errorf("nvenc: %w", err)
	}
	var max uint32
	r, _, _ := nvencDLL.NewProc("NvEncodeAPIGetMaxSupportedVersion").Call(uintptr(unsafe.Pointer(&max)))
	if r != statusSuccess {
		return nil, fmt.Errorf("nvenc: NvEncodeAPIGetMaxSupportedVersion status %d", r)
	}
	if major, minor := max>>4, max&0xf; major < apiMajor || major == apiMajor && minor < apiMinor {
		return nil, fmt.Errorf("nvenc: driver supports API %d.%d, need %d.%d", major, minor, apiMajor, apiMinor)
	}
	a := &api{}
	binary.LittleEndian.PutUint32(a.table[0:], verFunctionList)
	r, _, _ = nvencDLL.NewProc("NvEncodeAPICreateInstance").Call(uintptr(unsafe.Pointer(&a.table[0])))
	if r != statusSuccess {
		return nil, fmt.Errorf("nvenc: NvEncodeAPICreateInstance status %d", r)
	}
	loaded = a
	return a, nil
}

// MaxAPIVersion reports the NVENC API version the installed driver supports.
func MaxAPIVersion() (major, minor uint32, err error) {
	if err := nvencDLL.Load(); err != nil {
		return 0, 0, err
	}
	var max uint32
	r, _, _ := nvencDLL.NewProc("NvEncodeAPIGetMaxSupportedVersion").Call(uintptr(unsafe.Pointer(&max)))
	if r != statusSuccess {
		return 0, 0, fmt.Errorf("status %d", r)
	}
	return max >> 4, max & 0xf, nil
}

func put32(b []byte, off int, v uint32)     { binary.LittleEndian.PutUint32(b[off:], v) }
func put64(b []byte, off int, v uint64)     { binary.LittleEndian.PutUint64(b[off:], v) }
func get32(b []byte, off int) uint32        { return binary.LittleEndian.Uint32(b[off:]) }
func get64(b []byte, off int) uint64        { return binary.LittleEndian.Uint64(b[off:]) }
func putGUID(b []byte, off int, g win.GUID) { *(*win.GUID)(unsafe.Pointer(&b[off])) = g }

func setBits(b []byte, off int, shift, width uint, v uint32) {
	w := get32(b, off)
	mask := uint32(1<<width-1) << shift
	put32(b, off, w&^mask|v<<shift&mask)
}

// Options are NVENC specific knobs beyond encode.Config.
type Options struct {
	// SplitMode, when not 0, is the NV_ENC_SPLIT_ENCODE_MODE to use
	// whatever Config.Split says: 2 forces two engines, 15 disables.
	SplitMode uint32
}

// NV_ENC_SPLIT_ENCODE_MODE values.
const (
	splitDriverAuto = 0
	splitTwoForced  = 2
	splitDisabled   = 15
)

// splitMode maps Config.Split onto NVENC. SplitAuto below its thresholds
// leaves the driver's own rule, which splits only from 2112 lines.
func (e *Encoder) splitMode() uint32 {
	switch {
	case e.opts.SplitMode != 0:
		return e.opts.SplitMode
	case e.cfg.SplitFrame():
		return splitTwoForced
	case e.cfg.Split == encode.SplitOff:
		return splitDisabled
	}
	return splitDriverAuto
}

type registration struct {
	ptr    uintptr
	format uint32
}

type Encoder struct {
	api     *api
	enc     uintptr
	adapter gpu.Adapter
	cfg     encode.Config
	opts    Options
	codec   win.GUID

	initp  []byte
	config []byte
	pic    []byte
	lock   []byte
	mapp   []byte

	registered map[uintptr]registration
	output     uintptr
	event      windows.Handle
	frame      uint32

	sliceOffsets []uint32
	sliceEnds    []int
	firstSlice   time.Duration
	submitted    time.Duration
	// polls counts the bitstream locks of the last sub-frame encode.
	polls int
	caps  encode.Caps
}

// call keeps what pointer arguments point to on the heap and alive until
// it returns, so a stack move cannot leave NVENC writing to a dead stack.
//
//go:uintptrescapes
func (e *Encoder) call(offset int, args ...uintptr) error {
	r, _, _ := syscall.SyscallN(e.api.fn(offset), args...)
	return e.status(offset, r)
}

// call2 is call for the per-frame path, free of allocations. Pointers passed
// must be to the heap.
func (e *Encoder) call2(offset int, a, b uintptr) error {
	r, _, _ := syscall.SyscallN(e.api.fn(offset), a, b)
	return e.status(offset, r)
}

func (e *Encoder) status(offset int, r uintptr) error {
	if uint32(r) == statusSuccess {
		return nil
	}
	detail := ""
	if e.enc != 0 {
		p, _, _ := syscall.SyscallN(e.api.fn(fnGetLastErrorString), e.enc)
		detail = windows.BytePtrToString((*byte)(win.Ptr(p)))
	}
	return &Error{Fn: offset, Status: uint32(r), Detail: detail}
}

type Error struct {
	Fn     int
	Status uint32
	Detail string
}

func (e *Error) Error() string {
	return fmt.Sprintf("nvenc: function @%d status %d: %s", e.Fn, e.Status, e.Detail)
}

func openSession(a *api, device *win.Object) (uintptr, error) {
	p := make([]byte, sizeOpenSession)
	put32(p, 0, verOpenSession)
	put32(p, osDeviceType, deviceTypeDirectX)
	put64(p, osDevice, uint64(uintptr(unsafe.Pointer(device))))
	put32(p, osAPIVersion, apiVersion)
	var enc uintptr
	r, _, _ := syscall.SyscallN(a.fn(fnOpenEncodeSessionEx), uintptr(unsafe.Pointer(&p[0])), uintptr(unsafe.Pointer(&enc)))
	if uint32(r) != statusSuccess {
		return 0, &Error{Fn: fnOpenEncodeSessionEx, Status: uint32(r)}
	}
	return enc, nil
}

func codecGUID(c encode.Codec) (win.GUID, error) {
	switch c {
	case encode.HEVC:
		return guidHEVC, nil
	case encode.AV1:
		return guidAV1, nil
	case encode.H264:
		return guidH264, nil
	}
	return win.GUID{}, encode.ErrUnsupported
}

func (e *Encoder) capValue(codec win.GUID, which uint32) int {
	p := make([]byte, sizeCapsParam)
	put32(p, 0, verCapsParam)
	put32(p, capsToQuery, which)
	var v int32
	r, _, _ := syscall.SyscallN(e.api.fn(fnGetEncodeCaps), e.enc, uintptr(unsafe.Pointer(&codec)),
		uintptr(unsafe.Pointer(&p[0])), uintptr(unsafe.Pointer(&v)))
	if uint32(r) != statusSuccess {
		return 0
	}
	return int(v)
}

func (e *Encoder) queryCaps() encode.Caps {
	caps := encode.Caps{Adapter: e.adapter, Chroma: map[encode.Codec][]encode.Chroma{}}
	var n uint32
	syscall.SyscallN(e.api.fn(fnGetEncodeGUIDCount), e.enc, uintptr(unsafe.Pointer(&n)))
	guids := make([]win.GUID, n)
	if n > 0 {
		syscall.SyscallN(e.api.fn(fnGetEncodeGUIDs), e.enc, uintptr(unsafe.Pointer(&guids[0])), uintptr(n), uintptr(unsafe.Pointer(&n)))
	}
	for _, g := range guids[:n] {
		var c encode.Codec
		switch g {
		case guidH264:
			c = encode.H264
		case guidHEVC:
			c = encode.HEVC
		case guidAV1:
			c = encode.AV1
		default:
			continue
		}
		caps.Codecs = append(caps.Codecs, c)
		chroma := []encode.Chroma{encode.NV12}
		ten := e.capValue(g, capsSupport10Bit) != 0
		full := e.capValue(g, capsSupportYUV444) != 0
		if ten {
			chroma = append(chroma, encode.P010)
		}
		if full {
			chroma = append(chroma, encode.YUV444)
		}
		if ten && full && c != encode.H264 {
			chroma = append(chroma, encode.YUV444P10)
		}
		caps.Chroma[c] = chroma
		if c == encode.HEVC {
			caps.MaxWidth = e.capValue(g, capsWidthMax)
			caps.MaxHeight = e.capValue(g, capsHeightMax)
			caps.IntraRefresh = e.capValue(g, capsSupportIntraRefresh) != 0
			caps.SubFrame = e.capValue(g, capsSupportSubframeReadback) != 0
			caps.Engines = e.capValue(g, capsNumEncoderEngines)
		}
	}
	return caps
}

// Probe opens a session on device and reports what NVENC offers there.
func Probe(device win.Device, adapter gpu.Adapter) (encode.Caps, error) {
	a, err := loadAPI()
	if err != nil {
		return encode.Caps{}, err
	}
	enc, err := openSession(a, device.Object)
	if err != nil {
		return encode.Caps{}, err
	}
	e := &Encoder{api: a, enc: enc, adapter: adapter}
	defer syscall.SyscallN(a.fn(fnDestroyEncoder), enc)
	return e.queryCaps(), nil
}

// Open starts an encoder on device. Frames passed to Encode must be
// ID3D11Texture2D surfaces created on the same device.
func Open(device win.Device, adapter gpu.Adapter, cfg encode.Config, opts Options) (*Encoder, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if cfg.Adapter != 0 && cfg.Adapter != adapter.LUID {
		return nil, errors.New("nvenc: encode adapter must own the device; copy frames across adapters first")
	}
	a, err := loadAPI()
	if err != nil {
		return nil, err
	}
	codec, err := codecGUID(cfg.Codec)
	if err != nil {
		return nil, err
	}
	enc, err := openSession(a, device.Object)
	if err != nil {
		return nil, err
	}
	e := &Encoder{
		api: a, enc: enc, adapter: adapter, cfg: cfg, opts: opts, codec: codec,
		initp:      make([]byte, sizeInitialize),
		config:     make([]byte, sizeConfig),
		pic:        make([]byte, sizePicParams),
		lock:       make([]byte, sizeLockBitstream),
		mapp:       make([]byte, sizeMapInput),
		registered: map[uintptr]registration{},
	}
	e.caps = e.queryCaps()
	e.caps.RefInvalidation = e.capValue(codec, capsSupportRefPicInvalidation) != 0
	if err := e.initialize(); err != nil {
		e.Close()
		return nil, err
	}
	return e, nil
}

func (e *Encoder) supports(c encode.Chroma) bool {
	for _, have := range e.caps.Chroma[e.cfg.Codec] {
		if have == c {
			return true
		}
	}
	return false
}

func (e *Encoder) initialize() error {
	cfg := e.cfg
	if !e.supports(cfg.Chroma) {
		return fmt.Errorf("%w: %s %s on %s", encode.ErrUnsupported, cfg.Codec, cfg.Chroma, e.adapter.Name)
	}
	if cfg.SubFrame && !e.caps.SubFrame {
		return fmt.Errorf("%w: sub-frame readback", encode.ErrUnsupported)
	}
	preset := make([]byte, sizePresetConfig)
	put32(preset, 0, verPresetConfig)
	put32(preset, pcPresetCfg, verConfig)
	codec, presetGUID := e.codec, guidPresetP1
	if err := e.call(fnGetEncodePresetConfigEx, e.enc, uintptr(unsafe.Pointer(&codec)),
		uintptr(unsafe.Pointer(&presetGUID)), tuningUltraLowLatency, uintptr(unsafe.Pointer(&preset[0]))); err != nil {
		return err
	}
	c := e.config
	copy(c, preset[pcPresetCfg:pcPresetCfg+sizeConfig])
	put32(c, 0, verConfig)
	put32(c, cfgGopLength, infiniteGOP)
	put32(c, cfgFrameIntervalP, 1)

	bitrate := uint32(cfg.BitrateKbps) * 1000
	rc := c[cfgRC:]
	put32(rc, rcVersion, verRCParams)
	put32(rc, rcMode, rcCBR)
	put32(rc, rcAvgBitrate, bitrate)
	put32(rc, rcMaxBitrate, bitrate)
	put32(rc, rcVBVSize, bitrate/uint32(cfg.FPS))
	put32(rc, rcVBVInit, bitrate/uint32(cfg.FPS))

	inDepth, outDepth := uint32(bitDepth8), uint32(bitDepth8)
	if cfg.Source == capture.FormatP010 || cfg.Source == capture.FormatRGB10A2 {
		inDepth = bitDepth10
	}
	if cfg.Chroma.TenBit() {
		outDepth = bitDepth10
	}
	chromaIDC := uint32(1)
	if cfg.Chroma.Full() {
		chromaIDC = 3
	}
	cc := c[cfgCodec:]
	switch cfg.Codec {
	case encode.HEVC:
		profile := guidHEVCMain
		switch {
		case cfg.Chroma.Full():
			profile = guidHEVCFRExt
		case cfg.Chroma.TenBit():
			profile = guidHEVCMain10
		}
		putGUID(c, cfgProfileGUID, profile)
		setBits(cc, hevcBits, hevcBitRepeatSPSPPS, 1, 1)
		setBits(cc, hevcBits, hevcBitChromaFormat, 2, chromaIDC)
		put32(cc, hevcIdrPeriod, infiniteGOP)
		put32(cc, hevcInputBitDepth, inDepth)
		put32(cc, hevcOutputBitDepth, outDepth)
		if cfg.IntraRefreshPeriod > 0 {
			setBits(cc, hevcBits, hevcBitIntraRefresh, 1, 1)
			put32(cc, hevcIntraRefreshPeriod, uint32(cfg.IntraRefreshPeriod))
			put32(cc, hevcIntraRefreshCnt, uint32(max(cfg.IntraRefreshPeriod/2, 1)))
		}
		if cfg.Slices > 1 {
			put32(cc, hevcSliceMode, 3)
			put32(cc, hevcSliceModeData, uint32(cfg.Slices))
		}
		if e.caps.RefInvalidation {
			put32(cc, hevcMaxRefDPB, refDPB)
		}
	case encode.AV1:
		if cfg.Chroma.Full() {
			return fmt.Errorf("%w: AV1 4:4:4 needs the high profile", encode.ErrUnsupported)
		}
		putGUID(c, cfgProfileGUID, guidAV1Main)
		setBits(cc, av1Bits, av1BitRepeatSeqHdr, 1, 1)
		setBits(cc, av1Bits, av1BitChromaFormat, 2, chromaIDC)
		put32(cc, av1IdrPeriod, infiniteGOP)
		put32(cc, av1InputBitDepth, inDepth)
		put32(cc, av1OutputBitDepth, outDepth)
		if cfg.IntraRefreshPeriod > 0 {
			setBits(cc, av1Bits, av1BitIntraRefresh, 1, 1)
			put32(cc, av1IntraRefreshPeriod, uint32(cfg.IntraRefreshPeriod))
			put32(cc, av1IntraRefreshCnt, uint32(max(cfg.IntraRefreshPeriod/2, 1)))
		}
		if cfg.Slices > 1 {
			put32(cc, av1NumTileRows, uint32(cfg.Slices))
			put32(cc, av1NumTileColumns, 1)
		}
		if e.caps.RefInvalidation {
			put32(cc, av1MaxRefDPB, refDPB)
		}
	default:
		return encode.ErrUnsupported
	}

	p := e.initp
	put32(p, 0, verInitialize)
	putGUID(p, initEncodeGUID, e.codec)
	putGUID(p, initPresetGUID, guidPresetP1)
	put32(p, initWidth, uint32(cfg.Width))
	put32(p, initHeight, uint32(cfg.Height))
	put32(p, initDarWidth, uint32(cfg.Width))
	put32(p, initDarHeight, uint32(cfg.Height))
	put32(p, initFrameRateNum, uint32(cfg.FPS))
	put32(p, initFrameRateDen, 1)
	put32(p, initPTD, 1)
	put32(p, initMaxWidth, uint32(cfg.Width))
	put32(p, initMaxHeight, uint32(cfg.Height))
	put32(p, initTuningInfo, tuningUltraLowLatency)
	put64(p, initEncodeConfig, uint64(uintptr(unsafe.Pointer(&c[0]))))
	setBits(p, initBits, initBitSplitEncode, 4, e.splitMode())
	if cfg.SubFrame {
		// Sub-frame readback polls nvEncLockBitstream, which NVENC allows in
		// synchronous mode only.
		setBits(p, initBits, initBitReportSliceOffsets, 1, 1)
		setBits(p, initBits, initBitSubFrameWrite, 1, 1)
	} else {
		put32(p, initEncodeAsync, 1)
	}
	if err := e.call(fnInitializeEncoder, e.enc, uintptr(unsafe.Pointer(&p[0]))); err != nil {
		return err
	}
	if !cfg.SubFrame {
		ev, err := windows.CreateEvent(nil, 0, 0, nil)
		if err != nil {
			return err
		}
		e.event = ev
		evp := make([]byte, sizeEventParams)
		put32(evp, 0, verEventParams)
		put64(evp, evEvent, uint64(ev))
		if err := e.call(fnRegisterAsyncEvent, e.enc, uintptr(unsafe.Pointer(&evp[0]))); err != nil {
			return err
		}
	}
	cb := make([]byte, sizeCreateBitstream)
	put32(cb, 0, verCreateBitstream)
	if err := e.call(fnCreateBitstreamBuffer, e.enc, uintptr(unsafe.Pointer(&cb[0]))); err != nil {
		return err
	}
	e.output = uintptr(get64(cb, cbBitstream))
	slices := max(cfg.Slices, 1)
	e.sliceOffsets = make([]uint32, slices)
	e.sliceEnds = make([]int, 0, slices)
	return nil
}

func bufferFormat(f capture.Format) (uint32, error) {
	switch f {
	case capture.FormatBGRA8:
		return bufARGB, nil
	case capture.FormatRGB10A2:
		return bufABGR10, nil
	case capture.FormatNV12:
		return bufNV12, nil
	case capture.FormatP010:
		return bufYUV42010, nil
	}
	return 0, fmt.Errorf("%w: input format %s", encode.ErrUnsupported, f)
}

func (e *Encoder) register(f capture.Frame) (registration, error) {
	if r, ok := e.registered[f.Surface]; ok {
		return r, nil
	}
	format, err := bufferFormat(f.Format)
	if err != nil {
		return registration{}, err
	}
	p := make([]byte, sizeRegister)
	put32(p, 0, verRegister)
	put32(p, regResourceType, resourceDirectX)
	put32(p, regWidth, uint32(f.Width))
	put32(p, regHeight, uint32(f.Height))
	put64(p, regResource, uint64(f.Surface))
	put32(p, regBufferFormat, format)
	put32(p, regBufferUsage, usageInputImage)
	if err := e.call(fnRegisterResource, e.enc, uintptr(unsafe.Pointer(&p[0]))); err != nil {
		return registration{}, err
	}
	r := registration{ptr: uintptr(get64(p, regRegistered)), format: format}
	e.registered[f.Surface] = r
	return r, nil
}

func (e *Encoder) Caps() encode.Caps { return e.caps }

// FirstSlice is how long after submission the first slice of the last
// sub-frame encode became readable, and Submitted is when Encode submitted it.
func (e *Encoder) FirstSlice() time.Duration { return e.firstSlice }

func (e *Encoder) Encode(f capture.Frame, forceKey bool, dst []byte) (encode.Packet, error) {
	return e.EncodeSlices(f, forceKey, dst, nil)
}

// EncodeSlices emits slices as they finish when the encoder was opened
// with Config.SubFrame; otherwise it emits nothing and is Encode.
func (e *Encoder) EncodeSlices(f capture.Frame, forceKey bool, dst []byte, emit func(int, []byte, bool) error) (encode.Packet, error) {
	if !e.cfg.SubFrame {
		emit = nil
	}
	reg, err := e.register(f)
	if err != nil {
		return encode.Packet{}, err
	}
	m := e.mapp
	clear(m)
	put32(m, 0, verMapInput)
	put64(m, mapRegistered, uint64(reg.ptr))
	if err := e.call2(fnMapInputResource, e.enc, uintptr(unsafe.Pointer(&m[0]))); err != nil {
		return encode.Packet{}, err
	}
	mapped := uintptr(get64(m, mapMapped))
	pkt, err := e.encodeMapped(f, mapped, get32(m, mapMappedFormat), forceKey, dst, emit)
	if uerr := e.call2(fnUnmapInputResource, e.enc, mapped); err == nil {
		err = uerr
	}
	return pkt, err
}

func (e *Encoder) encodeMapped(f capture.Frame, mapped uintptr, format uint32, forceKey bool, dst []byte, emit func(int, []byte, bool) error) (encode.Packet, error) {
	p := e.pic
	clear(p)
	put32(p, 0, verPicParams)
	put32(p, picInputWidth, uint32(f.Width))
	put32(p, picInputHeight, uint32(f.Height))
	put32(p, picInputPitch, uint32(f.Width))
	if forceKey {
		put32(p, picFlags, picFlagForceIDR|picFlagOutputSPSPPS)
	}
	put32(p, picFrameIdx, e.frame)
	put64(p, picTimeStamp, uint64(e.frame))
	put64(p, picInputBuffer, uint64(mapped))
	put64(p, picOutput, uint64(e.output))
	put64(p, picEvent, uint64(e.event))
	put32(p, picBufferFmt, format)
	put32(p, picStruct, picStructFrame)
	e.frame++

	e.submitted = clock.Now()
	if err := e.call2(fnEncodePicture, e.enc, uintptr(unsafe.Pointer(&p[0]))); err != nil {
		return encode.Packet{}, err
	}
	l := e.lock
	clear(l)
	put32(l, 0, verLockBitstream)
	put64(l, lockOutput, uint64(e.output))
	put64(l, lockSliceOffsets, uint64(uintptr(unsafe.Pointer(&e.sliceOffsets[0]))))
	dst = dst[:0]
	if e.cfg.SubFrame {
		if err := e.pollSubFrame(&dst, forceKey, emit); err != nil {
			return encode.Packet{}, err
		}
	} else {
		ev, err := windows.WaitForSingleObject(e.event, 2000)
		if err != nil || ev != windows.WAIT_OBJECT_0 {
			return encode.Packet{}, fmt.Errorf("nvenc: frame not done in 2 s (wait=%d err=%v)", ev, err)
		}
		if err := e.call2(fnLockBitstream, e.enc, uintptr(unsafe.Pointer(&l[0]))); err != nil {
			return encode.Packet{}, err
		}
	}
	size := int(get32(l, lockSize))
	data := unsafe.Slice((*byte)(win.Ptr(uintptr(get64(l, lockPtr)))), size)
	pkt := encode.Packet{
		Data:     append(dst, data[len(dst):]...),
		Keyframe: isKeyframe(l),
		PTS:      int64(get64(l, lockTimeStamp)),
	}
	if e.cfg.Slices > 1 && e.cfg.Codec == encode.HEVC && e.cfg.SubFrame {
		e.sliceEnds = e.sliceEnds[:0]
		n := int(get32(l, lockNumSlices))
		for i := 1; i < n && i < len(e.sliceOffsets); i++ {
			e.sliceEnds = append(e.sliceEnds, int(e.sliceOffsets[i]))
		}
		e.sliceEnds = append(e.sliceEnds, size)
		pkt.SliceEnds = e.sliceEnds
	}
	err := e.call2(fnUnlockBitstream, e.enc, e.output)
	return pkt, err
}

func isKeyframe(lock []byte) bool {
	t := get32(lock, lockPictureType)
	return t == picTypeIDR || t == picTypeI
}

// pollGap spaces the bitstream locks of a sub-frame encode. Each lock is a
// driver call; between them the thread yields its core. A slice takes
// about 150 to 350 us at 1080p (Phase 1 audit), so the gap delays one by
// at most a sixth of that.
const pollGap = 25 * time.Microsecond

var procSwitchToThread = windows.NewLazySystemDLL("kernel32.dll").NewProc("SwitchToThread")

func relax(since time.Duration) {
	for clock.Now()-since < pollGap {
		syscall.SyscallN(procSwitchToThread.Addr())
	}
}

// pollSubFrame locks the bitstream without waiting until the whole picture
// is done, recording when the first slice became readable. With emit it
// copies each slice into *dst as it finishes and hands it on. It returns
// with the bitstream locked.
func (e *Encoder) pollSubFrame(dst *[]byte, forceKey bool, emit func(int, []byte, bool) error) error {
	l := e.lock
	setBits(l, lockBits, lockBitDoNotWait, 1, 1)
	e.firstSlice, e.polls = 0, 0
	want := max(e.cfg.Slices, 1)
	emitted := 0
	for {
		polled := clock.Now()
		r, _, _ := syscall.SyscallN(e.api.fn(fnLockBitstream), e.enc, uintptr(unsafe.Pointer(&l[0])))
		e.polls++
		switch uint32(r) {
		case statusSuccess:
		case statusLockBusy, statusEncoderBusy:
			relax(polled)
			continue
		default:
			return e.status(fnLockBitstream, r)
		}
		n := min(int(get32(l, lockNumSlices)), want)
		if n > 0 && e.firstSlice == 0 {
			e.firstSlice = clock.Now() - e.submitted
		}
		done := get32(l, lockHWStatus) == 2 || n >= want
		if emit != nil && n > emitted {
			if err := e.emitSlices(dst, emitted, n, forceKey || isKeyframe(l), emit); err != nil {
				e.call2(fnUnlockBitstream, e.enc, e.output)
				return err
			}
			emitted = n
		}
		if done {
			return nil
		}
		if err := e.call2(fnUnlockBitstream, e.enc, e.output); err != nil {
			return err
		}
		relax(polled)
	}
}

// emitSlices copies the finished slices from..to-1 out of the locked
// bitstream, whose size counts the bytes of every finished slice.
func (e *Encoder) emitSlices(dst *[]byte, from, to int, key bool, emit func(int, []byte, bool) error) error {
	l := e.lock
	size := int(get32(l, lockSize))
	data := unsafe.Slice((*byte)(win.Ptr(uintptr(get64(l, lockPtr)))), size)
	*dst = append(*dst, data[len(*dst):]...)
	for i := from; i < to; i++ {
		end := size
		if i+1 < to {
			end = int(e.sliceOffsets[i+1])
		}
		if err := emit(i, (*dst)[e.sliceOffsets[i]:end], key); err != nil {
			return err
		}
	}
	return nil
}

// refDPB is how many past pictures stay referable, so InvalidateRefs can
// fall back to one the receiver still holds instead of a keyframe.
const refDPB = 5

// InvalidateRefs stops the encoder predicting from the picture with this
// PTS and later ones, so the next picture references an older one that the
// receiver still holds.
func (e *Encoder) InvalidateRefs(pts int64) error {
	if !e.caps.RefInvalidation {
		return fmt.Errorf("%w: reference invalidation", encode.ErrUnsupported)
	}
	return e.call2(fnInvalidateRefFrames, e.enc, uintptr(pts))
}

func (e *Encoder) SetBitrate(kbps int) error {
	if kbps <= 0 {
		return fmt.Errorf("nvenc: bitrate %d must be positive", kbps)
	}
	bitrate := uint32(kbps) * 1000
	rc := e.config[cfgRC:]
	put32(rc, rcAvgBitrate, bitrate)
	put32(rc, rcMaxBitrate, bitrate)
	put32(rc, rcVBVSize, bitrate/uint32(e.cfg.FPS))
	put32(rc, rcVBVInit, bitrate/uint32(e.cfg.FPS))
	p := make([]byte, sizeReconfigure)
	put32(p, 0, verReconfigure)
	copy(p[reconfInit:], e.initp)
	if err := e.call(fnReconfigureEncoder, e.enc, uintptr(unsafe.Pointer(&p[0]))); err != nil {
		return err
	}
	e.cfg.BitrateKbps = kbps
	return nil
}

func (e *Encoder) Close() error {
	if e.enc == 0 {
		return nil
	}
	for _, r := range e.registered {
		syscall.SyscallN(e.api.fn(fnUnregisterResource), e.enc, r.ptr)
	}
	if e.output != 0 {
		syscall.SyscallN(e.api.fn(fnDestroyBitstreamBuffer), e.enc, e.output)
	}
	if e.event != 0 {
		evp := make([]byte, sizeEventParams)
		put32(evp, 0, verEventParams)
		put64(evp, evEvent, uint64(e.event))
		syscall.SyscallN(e.api.fn(fnUnregisterAsyncEvent), e.enc, uintptr(unsafe.Pointer(&evp[0])))
		windows.CloseHandle(e.event)
	}
	syscall.SyscallN(e.api.fn(fnDestroyEncoder), e.enc)
	e.enc = 0
	return nil
}

var (
	_ encode.Encoder        = (*Encoder)(nil)
	_ encode.RefInvalidator = (*Encoder)(nil)
)
