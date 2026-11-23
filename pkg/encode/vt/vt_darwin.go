//go:build darwin

// Package vt drives Apple VideoToolbox hardware HEVC encode and decode
// through purego, with no cgo. Output and decode callbacks arrive on
// VideoToolbox's own threads and hand off to Go over channels.
package vt

import (
	"fmt"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"

	"github.com/watzon/planck/internal/apple"
	"github.com/watzon/planck/pkg/capture"
	"github.com/watzon/planck/pkg/encode"
)

const (
	codecHEVC = 0x68766331 // 'hvc1'

	PixelBGRA = 0x42475241 // 'BGRA'
	Pixel420v = 0x34323076 // '420v' NV12 video range
	PixelX420 = 0x78343230 // 'x420' 10-bit 4:2:0 video range (P010 layout)
	PixelL10r = 0x6c313072 // 'l10r' ARGB 2:10:10:10 little endian
)

var (
	vtCompressionSessionCreate                func(alloc uintptr, w, h int32, codec uint32, spec, srcAttrs, outAlloc, cb, refcon uintptr, out *uintptr) int32
	vtSessionSetProperty                      func(s, key, value uintptr) int32
	vtCompressionSessionPrepareToEncodeFrames func(s uintptr) int32
	vtCompressionSessionEncodeFrame           func(s, image uintptr, pts, dur apple.CMTime, props, refcon uintptr, flags *uint32) int32
	vtCompressionSessionCompleteFrames        func(s uintptr, until apple.CMTime) int32
	vtCompressionSessionInvalidate            func(s uintptr)
	vtDecompressionSessionCreate              func(alloc, format, spec, dstAttrs uintptr, cb unsafe.Pointer, out *uintptr) int32
	vtDecompressionSessionDecodeFrame         func(s, sbuf uintptr, flags uint32, refcon uintptr, infoOut *uint32) int32
	vtDecompressionSessionInvalidate          func(s uintptr)
	vtIsHardwareDecodeSupported               func(codec uint32) bool

	cmSampleBufferGetFormatDescription      func(sbuf uintptr) uintptr
	cmSampleBufferGetDataBuffer             func(sbuf uintptr) uintptr
	cmSampleBufferGetSampleAttachmentsArray func(sbuf uintptr, create bool) uintptr
	cmBlockBufferGetDataLength              func(bb uintptr) uint
	cmBlockBufferCopyDataBytes              func(bb uintptr, offset, length uint, dst unsafe.Pointer) int32

	cvPixelBufferCreate                func(alloc uintptr, w, h uint, format uint32, attrs uintptr, out *uintptr) int32
	cvPixelBufferLockBaseAddress       func(pb uintptr, flags uint64) int32
	cvPixelBufferUnlockBaseAddress     func(pb uintptr, flags uint64) int32
	cvPixelBufferGetBaseAddressOfPlane func(pb uintptr, plane uint) uintptr
	cvPixelBufferGetBytesPerRowOfPlane func(pb uintptr, plane uint) uint
	cvPixelBufferGetPlaneCount         func(pb uintptr) uint
	cvPixelBufferGetHeightOfPlane      func(pb uintptr, plane uint) uint
	cvPixelBufferGetBaseAddress        func(pb uintptr) uintptr
	cvPixelBufferGetBytesPerRow        func(pb uintptr) uint
	cvPixelBufferGetHeight             func(pb uintptr) uint
)

var keys struct {
	requireHW, lowLatency, realTime, allowReorder, profileLevel, avgBitrate, expectedFPS,
	maxKeyInterval, prioritizeSpeed, maxFrameDelay, dataRateLimits, notSync,
	ioSurfaceProps, pixelFormat, width, height uintptr
	hevcMain, hevcMain10, hevcMain42210 uintptr
}

func init() {
	apple.Register(&vtCompressionSessionCreate, apple.VideoToolbox, "VTCompressionSessionCreate")
	apple.Register(&vtSessionSetProperty, apple.VideoToolbox, "VTSessionSetProperty")
	apple.Register(&vtCompressionSessionPrepareToEncodeFrames, apple.VideoToolbox, "VTCompressionSessionPrepareToEncodeFrames")
	apple.Register(&vtCompressionSessionEncodeFrame, apple.VideoToolbox, "VTCompressionSessionEncodeFrame")
	apple.Register(&vtCompressionSessionCompleteFrames, apple.VideoToolbox, "VTCompressionSessionCompleteFrames")
	apple.Register(&vtCompressionSessionInvalidate, apple.VideoToolbox, "VTCompressionSessionInvalidate")
	apple.Register(&vtDecompressionSessionCreate, apple.VideoToolbox, "VTDecompressionSessionCreate")
	apple.Register(&vtDecompressionSessionDecodeFrame, apple.VideoToolbox, "VTDecompressionSessionDecodeFrame")
	apple.Register(&vtDecompressionSessionInvalidate, apple.VideoToolbox, "VTDecompressionSessionInvalidate")
	apple.Register(&vtIsHardwareDecodeSupported, apple.VideoToolbox, "VTIsHardwareDecodeSupported")
	apple.Register(&cmSampleBufferGetFormatDescription, apple.CoreMedia, "CMSampleBufferGetFormatDescription")
	apple.Register(&cmSampleBufferGetDataBuffer, apple.CoreMedia, "CMSampleBufferGetDataBuffer")
	apple.Register(&cmSampleBufferGetSampleAttachmentsArray, apple.CoreMedia, "CMSampleBufferGetSampleAttachmentsArray")
	apple.Register(&cmBlockBufferGetDataLength, apple.CoreMedia, "CMBlockBufferGetDataLength")
	apple.Register(&cmBlockBufferCopyDataBytes, apple.CoreMedia, "CMBlockBufferCopyDataBytes")
	apple.Register(&cvPixelBufferCreate, apple.CoreVideo, "CVPixelBufferCreate")
	apple.Register(&cvPixelBufferLockBaseAddress, apple.CoreVideo, "CVPixelBufferLockBaseAddress")
	apple.Register(&cvPixelBufferUnlockBaseAddress, apple.CoreVideo, "CVPixelBufferUnlockBaseAddress")
	apple.Register(&cvPixelBufferGetBaseAddressOfPlane, apple.CoreVideo, "CVPixelBufferGetBaseAddressOfPlane")
	apple.Register(&cvPixelBufferGetBytesPerRowOfPlane, apple.CoreVideo, "CVPixelBufferGetBytesPerRowOfPlane")
	apple.Register(&cvPixelBufferGetPlaneCount, apple.CoreVideo, "CVPixelBufferGetPlaneCount")
	apple.Register(&cvPixelBufferGetHeightOfPlane, apple.CoreVideo, "CVPixelBufferGetHeightOfPlane")
	apple.Register(&cvPixelBufferGetBaseAddress, apple.CoreVideo, "CVPixelBufferGetBaseAddress")
	apple.Register(&cvPixelBufferGetBytesPerRow, apple.CoreVideo, "CVPixelBufferGetBytesPerRow")
	apple.Register(&cvPixelBufferGetHeight, apple.CoreVideo, "CVPixelBufferGetHeight")

	vt, cv, cm := apple.VideoToolbox, apple.CoreVideo, apple.CoreMedia
	keys.requireHW = apple.Const(vt, "kVTVideoEncoderSpecification_RequireHardwareAcceleratedVideoEncoder")
	keys.lowLatency = apple.Const(vt, "kVTVideoEncoderSpecification_EnableLowLatencyRateControl")
	keys.realTime = apple.Const(vt, "kVTCompressionPropertyKey_RealTime")
	keys.allowReorder = apple.Const(vt, "kVTCompressionPropertyKey_AllowFrameReordering")
	keys.profileLevel = apple.Const(vt, "kVTCompressionPropertyKey_ProfileLevel")
	keys.avgBitrate = apple.Const(vt, "kVTCompressionPropertyKey_AverageBitRate")
	keys.expectedFPS = apple.Const(vt, "kVTCompressionPropertyKey_ExpectedFrameRate")
	keys.maxKeyInterval = apple.Const(vt, "kVTCompressionPropertyKey_MaxKeyFrameInterval")
	keys.prioritizeSpeed = apple.Const(vt, "kVTCompressionPropertyKey_PrioritizeEncodingSpeedOverQuality")
	keys.maxFrameDelay = apple.Const(vt, "kVTCompressionPropertyKey_MaxFrameDelayCount")
	keys.hevcMain = apple.Const(vt, "kVTProfileLevel_HEVC_Main_AutoLevel")
	keys.hevcMain10 = apple.Const(vt, "kVTProfileLevel_HEVC_Main10_AutoLevel")
	if apple.HasSym(vt, "kVTProfileLevel_HEVC_Main42210_AutoLevel") {
		keys.hevcMain42210 = apple.Const(vt, "kVTProfileLevel_HEVC_Main42210_AutoLevel")
	}
	keys.notSync = apple.Const(cm, "kCMSampleAttachmentKey_NotSync")
	keys.ioSurfaceProps = apple.Const(cv, "kCVPixelBufferIOSurfacePropertiesKey")
	keys.pixelFormat = apple.Const(cv, "kCVPixelBufferPixelFormatTypeKey")
	keys.width = apple.Const(cv, "kCVPixelBufferWidthKey")
	keys.height = apple.Const(cv, "kCVPixelBufferHeightKey")
}

// output is one finished frame from the encoder callback thread.
type output struct {
	status int32
	sbuf   uintptr // retained; the receiver releases it
	doneAt int64
}

// sessions maps a callback refcon to its encoder so the single C callback can
// route frames without passing Go pointers through C.
var (
	sessionsMu sync.Mutex
	sessions   = map[uintptr]*Encoder{}
	nextRef    uintptr
	encodeCB   uintptr
	cbOnce     sync.Once
)

func encodeCallback(refcon, srcRefcon uintptr, status int32, flags uint32, sbuf uintptr) {
	sessionsMu.Lock()
	e := sessions[refcon]
	sessionsMu.Unlock()
	if e == nil {
		return
	}
	if sbuf != 0 {
		apple.CFRetain(sbuf)
	}
	e.out <- output{status: status, sbuf: sbuf, doneAt: apple.Now()}
}

type Encoder struct {
	session uintptr
	ref     uintptr
	cfg     encode.Config
	out     chan output
	frame   int64
	// Keep is set by callers that want the CMSampleBuffer of the last packet
	// (to feed a decoder); they release it.
	Keep       bool
	LastSample uintptr
	LastDone   int64
	LowLatency bool
}

// Open creates a hardware HEVC compression session. 4:4:4 is refused: Apple
// silicon exposes HEVC 4:2:0 and 4:2:2 only.
func Open(cfg encode.Config) (*Encoder, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if cfg.Codec != encode.HEVC {
		return nil, fmt.Errorf("%w: VideoToolbox spike covers HEVC only", encode.ErrUnsupported)
	}
	if cfg.Chroma.Full() {
		return nil, fmt.Errorf("%w: no HEVC 4:4:4 encoder on Apple silicon", encode.ErrUnsupported)
	}
	cbOnce.Do(func() { encodeCB = purego.NewCallback(encodeCallback) })
	e := &Encoder{cfg: cfg, out: make(chan output, 8)}
	sessionsMu.Lock()
	nextRef++
	e.ref = nextRef
	sessions[e.ref] = e
	sessionsMu.Unlock()

	create := func(lowLatency bool) int32 {
		spec := apple.Dict(keys.requireHW, apple.KCFBooleanTrue)
		if lowLatency {
			apple.CFDictionarySetValue(spec, keys.lowLatency, apple.KCFBooleanTrue)
		}
		defer apple.CFRelease(spec)
		return vtCompressionSessionCreate(0, int32(cfg.Width), int32(cfg.Height), codecHEVC, spec, 0, 0, encodeCB, e.ref, &e.session)
	}
	e.LowLatency = true
	if st := create(true); st != 0 {
		e.LowLatency = false
		if st2 := create(false); st2 != 0 {
			e.Close()
			return nil, fmt.Errorf("vt: VTCompressionSessionCreate: %d (low-latency attempt %d)", st2, st)
		}
	}
	profile := keys.hevcMain
	if cfg.Chroma.TenBit() {
		profile = keys.hevcMain10
	}
	bitrate := apple.Int(int64(cfg.BitrateKbps) * 1000)
	fps := apple.Float(float64(cfg.FPS))
	keyInt := apple.Int(1 << 30)
	zero := apple.Int(0)
	defer func() {
		for _, r := range []uintptr{bitrate, fps, keyInt, zero} {
			apple.CFRelease(r)
		}
	}()
	props := []struct {
		name     string
		key, val uintptr
		optional bool
	}{
		{"RealTime", keys.realTime, apple.KCFBooleanTrue, false},
		{"AllowFrameReordering", keys.allowReorder, apple.KCFBooleanFalse, false},
		{"ProfileLevel", keys.profileLevel, profile, false},
		{"AverageBitRate", keys.avgBitrate, bitrate, false},
		{"ExpectedFrameRate", keys.expectedFPS, fps, false},
		{"MaxKeyFrameInterval", keys.maxKeyInterval, keyInt, false},
		{"PrioritizeEncodingSpeedOverQuality", keys.prioritizeSpeed, apple.KCFBooleanTrue, true},
		{"MaxFrameDelayCount", keys.maxFrameDelay, zero, true},
	}
	for _, p := range props {
		if st := vtSessionSetProperty(e.session, p.key, p.val); st != 0 && !p.optional {
			e.Close()
			return nil, fmt.Errorf("vt: set %s: %d", p.name, st)
		}
	}
	if st := vtCompressionSessionPrepareToEncodeFrames(e.session); st != 0 {
		e.Close()
		return nil, fmt.Errorf("vt: prepare: %d", st)
	}
	return e, nil
}

func (e *Encoder) Caps() encode.Caps {
	return encode.Caps{Codecs: []encode.Codec{encode.HEVC},
		Chroma: map[encode.Codec][]encode.Chroma{encode.HEVC: {encode.NV12, encode.P010}}}
}

// Encode submits a CVPixelBuffer (frame.Surface) and waits for its output.
func (e *Encoder) Encode(f capture.Frame, forceKey bool, dst []byte) (encode.Packet, error) {
	pts := apple.Time(e.frame, int32(e.cfg.FPS))
	e.frame++
	var flags uint32
	if st := vtCompressionSessionEncodeFrame(e.session, f.Surface, pts, apple.CMTime{}, 0, 0, &flags); st != 0 {
		return encode.Packet{}, fmt.Errorf("vt: encode frame: %d", st)
	}
	o := <-e.out
	if o.status != 0 {
		return encode.Packet{}, fmt.Errorf("vt: callback status %d", o.status)
	}
	if o.sbuf == 0 {
		return encode.Packet{}, encode.ErrDropped
	}
	bb := cmSampleBufferGetDataBuffer(o.sbuf)
	n := cmBlockBufferGetDataLength(bb)
	if uint(cap(dst)) < n {
		dst = make([]byte, n)
	}
	dst = dst[:n]
	cmBlockBufferCopyDataBytes(bb, 0, n, unsafe.Pointer(&dst[0]))
	pkt := encode.Packet{Data: dst, Keyframe: isKey(o.sbuf), PTS: pts.Value}
	e.LastDone = o.doneAt
	if e.Keep {
		e.LastSample = o.sbuf
	} else {
		apple.CFRelease(o.sbuf)
	}
	return pkt, nil
}

func isKey(sbuf uintptr) bool {
	arr := cmSampleBufferGetSampleAttachmentsArray(sbuf, false)
	if arr == 0 || apple.CFArrayGetCount(arr) == 0 {
		return true
	}
	d := apple.CFArrayGetValueAtIndex(arr, 0)
	v := apple.CFDictionaryGetValue(d, keys.notSync)
	return v == 0 || v == apple.KCFBooleanFalse
}

func (e *Encoder) SetBitrate(kbps int) error {
	b := apple.Int(int64(kbps) * 1000)
	defer apple.CFRelease(b)
	if st := vtSessionSetProperty(e.session, keys.avgBitrate, b); st != 0 {
		return fmt.Errorf("vt: set bitrate: %d", st)
	}
	return nil
}

func (e *Encoder) Close() error {
	if e.session != 0 {
		vtCompressionSessionInvalidate(e.session)
		apple.CFRelease(e.session)
		e.session = 0
	}
	sessionsMu.Lock()
	delete(sessions, e.ref)
	sessionsMu.Unlock()
	return nil
}

var _ encode.Encoder = (*Encoder)(nil)

// Decoder is a hardware HEVC decompression session.
type Decoder struct {
	session uintptr
	ref     uintptr
	done    chan int64
	record  *[2]uintptr
}

var (
	decoders   = map[uintptr]*Decoder{}
	decodeCB   uintptr
	decodeOnce sync.Once
)

// The CMTime arguments are larger than 16 bytes, so arm64 passes them by
// reference: they arrive here as pointers.
func decodeCallback(refcon, srcRefcon uintptr, status int32, flags uint32, image, pts, dur uintptr) {
	sessionsMu.Lock()
	d := decoders[refcon]
	sessionsMu.Unlock()
	if d == nil {
		return
	}
	t := apple.Now()
	if status != 0 || image == 0 {
		t = -int64(status)
	}
	d.done <- t
}

// NewDecoder opens a hardware decoder for the format of an encoded sample.
func NewDecoder(sample uintptr) (*Decoder, error) {
	decodeOnce.Do(func() { decodeCB = purego.NewCallback(decodeCallback) })
	d := &Decoder{done: make(chan int64, 4), record: new([2]uintptr)}
	sessionsMu.Lock()
	nextRef++
	d.ref = nextRef
	decoders[d.ref] = d
	sessionsMu.Unlock()
	d.record[0], d.record[1] = decodeCB, d.ref
	format := cmSampleBufferGetFormatDescription(sample)
	attrs := apple.Dict(keys.ioSurfaceProps, apple.Dict())
	defer apple.CFRelease(attrs)
	if st := vtDecompressionSessionCreate(0, format, 0, attrs, unsafe.Pointer(d.record), &d.session); st != 0 {
		return nil, fmt.Errorf("vt: VTDecompressionSessionCreate: %d", st)
	}
	return d, nil
}

// Decode decodes one sample synchronously and returns when the decoded image
// callback fired, in mach-clock nanoseconds.
func (d *Decoder) Decode(sample uintptr) (int64, error) {
	var info uint32
	if st := vtDecompressionSessionDecodeFrame(d.session, sample, 0, 0, &info); st != 0 {
		return 0, fmt.Errorf("vt: decode: %d", st)
	}
	t := <-d.done
	if t < 0 {
		return 0, fmt.Errorf("vt: decode callback status %d", -t)
	}
	return t, nil
}

func (d *Decoder) Close() {
	if d.session != 0 {
		vtDecompressionSessionInvalidate(d.session)
		apple.CFRelease(d.session)
		d.session = 0
	}
	sessionsMu.Lock()
	delete(decoders, d.ref)
	sessionsMu.Unlock()
}

// HardwareDecode reports whether the GPU decodes HEVC.
func HardwareDecode() bool { return vtIsHardwareDecodeSupported(codecHEVC) }

// NewPixelBuffer allocates an IOSurface-backed CVPixelBuffer.
func NewPixelBuffer(w, h int, format uint32) (uintptr, error) {
	inner := apple.Dict()
	attrs := apple.Dict(keys.ioSurfaceProps, inner)
	defer apple.CFRelease(attrs)
	defer apple.CFRelease(inner)
	var pb uintptr
	if st := cvPixelBufferCreate(0, uint(w), uint(h), format, attrs, &pb); st != 0 {
		return 0, fmt.Errorf("vt: CVPixelBufferCreate: %d", st)
	}
	return pb, nil
}

// FillPixelBuffer copies tightly packed planes into the buffer, row by row.
func FillPixelBuffer(pb uintptr, planes [][]byte, rowBytes []int) {
	cvPixelBufferLockBaseAddress(pb, 0)
	defer cvPixelBufferUnlockBaseAddress(pb, 0)
	type plane struct {
		base         uintptr
		stride, rows int
	}
	var ps []plane
	if n := int(cvPixelBufferGetPlaneCount(pb)); n == 0 {
		ps = append(ps, plane{cvPixelBufferGetBaseAddress(pb), int(cvPixelBufferGetBytesPerRow(pb)), int(cvPixelBufferGetHeight(pb))})
	} else {
		for i := 0; i < n; i++ {
			ps = append(ps, plane{cvPixelBufferGetBaseAddressOfPlane(pb, uint(i)),
				int(cvPixelBufferGetBytesPerRowOfPlane(pb, uint(i))), int(cvPixelBufferGetHeightOfPlane(pb, uint(i)))})
		}
	}
	for i, p := range ps {
		if i >= len(planes) {
			break
		}
		dst := unsafe.Slice((*byte)(ptr(p.base)), p.stride*p.rows)
		for y := 0; y < p.rows && (y+1)*rowBytes[i] <= len(planes[i]); y++ {
			copy(dst[y*p.stride:y*p.stride+rowBytes[i]], planes[i][y*rowBytes[i]:])
		}
	}
}

func ptr(p uintptr) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(&p)) }
