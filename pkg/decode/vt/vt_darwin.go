//go:build darwin

package vt

import (
	"errors"
	"fmt"
	"sync"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"

	"github.com/watzon/planck/internal/apple"
	"github.com/watzon/planck/pkg/capture"
	"github.com/watzon/planck/pkg/decode"
	"github.com/watzon/planck/pkg/encode"
)

const (
	codecAV1 = 0x61763031 // 'av01'

	pixel420v = 0x34323076 // '420v' NV12 video range
	pixelx420 = 0x78343230 // 'x420' P010 video range

)

var (
	cmVideoFormatDescriptionCreateFromHEVCParameterSets func(alloc uintptr, count uint, sets, sizes unsafe.Pointer, nalLen int32, ext uintptr, out *uintptr) int32
	cmVideoFormatDescriptionCreate                      func(alloc uintptr, codec uint32, w, h int32, ext uintptr, out *uintptr) int32
	cmBlockBufferCreateWithMemoryBlock                  func(alloc, block uintptr, length uint, blockAlloc, custom uintptr, offset, dataLength uint, flags uint32, out *uintptr) int32
	cmBlockBufferReplaceDataBytes                       func(src unsafe.Pointer, bb uintptr, offset, length uint) int32
	cmSampleBufferCreateReady                           func(alloc, bb, format uintptr, samples int, timings int, timing unsafe.Pointer, sizes int, size unsafe.Pointer, out *uintptr) int32
	vtDecompressionSessionCreate                        func(alloc, format, spec, dstAttrs uintptr, cb unsafe.Pointer, out *uintptr) int32
	vtDecompressionSessionDecodeFrame                   func(s, sbuf uintptr, flags uint32, refcon uintptr, infoOut *uint32) int32
	vtDecompressionSessionCanAcceptFormatDescription    func(s, format uintptr) bool
	vtDecompressionSessionInvalidate                    func(s uintptr)
	cvPixelBufferGetIOSurface                           func(pb uintptr) uintptr
	cvPixelBufferGetPixelFormatType                     func(pb uintptr) uint32
	cvPixelBufferGetWidth                               func(pb uintptr) uint
	cvPixelBufferGetHeight                              func(pb uintptr) uint
	cfDataCreate                                        func(alloc uintptr, p unsafe.Pointer, n int) uintptr

	keyAtoms, keyIOSurface, keyPixelFormat uintptr
)

func init() {
	cm, cv, vt := apple.CoreMedia, apple.CoreVideo, apple.VideoToolbox
	apple.Register(&cmVideoFormatDescriptionCreateFromHEVCParameterSets, cm, "CMVideoFormatDescriptionCreateFromHEVCParameterSets")
	apple.Register(&cmVideoFormatDescriptionCreate, cm, "CMVideoFormatDescriptionCreate")
	apple.Register(&cmBlockBufferCreateWithMemoryBlock, cm, "CMBlockBufferCreateWithMemoryBlock")
	apple.Register(&cmBlockBufferReplaceDataBytes, cm, "CMBlockBufferReplaceDataBytes")
	apple.Register(&cmSampleBufferCreateReady, cm, "CMSampleBufferCreateReady")
	apple.Register(&vtDecompressionSessionCreate, vt, "VTDecompressionSessionCreate")
	apple.Register(&vtDecompressionSessionDecodeFrame, vt, "VTDecompressionSessionDecodeFrame")
	apple.Register(&vtDecompressionSessionCanAcceptFormatDescription, vt, "VTDecompressionSessionCanAcceptFormatDescription")
	apple.Register(&vtDecompressionSessionInvalidate, vt, "VTDecompressionSessionInvalidate")
	apple.Register(&cvPixelBufferGetIOSurface, cv, "CVPixelBufferGetIOSurface")
	apple.Register(&cvPixelBufferGetPixelFormatType, cv, "CVPixelBufferGetPixelFormatType")
	apple.Register(&cvPixelBufferGetWidth, cv, "CVPixelBufferGetWidth")
	apple.Register(&cvPixelBufferGetHeight, cv, "CVPixelBufferGetHeight")
	apple.Register(&cfDataCreate, apple.CoreFoundation, "CFDataCreate")
	keyAtoms = apple.Const(cm, "kCMFormatDescriptionExtension_SampleDescriptionExtensionAtoms")
	keyIOSurface = apple.Const(cv, "kCVPixelBufferIOSurfacePropertiesKey")
	keyPixelFormat = apple.Const(cv, "kCVPixelBufferPixelFormatTypeKey")
}

// decoded is the decode callback's result, from VideoToolbox's thread.
type decoded struct {
	status int32
	image  uintptr // retained; the receiver releases it
	pts    int64
	at     int64
}

var (
	decodersMu sync.Mutex
	decoders   = map[uintptr]*Decoder{}
	nextRef    uintptr
	callback   uintptr
	cbOnce     sync.Once
)

// The CMTime arguments are larger than 16 bytes, so arm64 passes them by
// reference: they arrive here as pointers.
func onDecoded(refcon, _ uintptr, status int32, _ uint32, image, pts, _ uintptr) {
	decodersMu.Lock()
	d := decoders[refcon]
	decodersMu.Unlock()
	if d == nil {
		return
	}
	r := decoded{status: status, at: apple.Now()}
	if image != 0 && status == 0 {
		r.image = apple.CFRetain(image)
	}
	if pts != 0 {
		r.pts = (*apple.CMTime)(cMemory(pts)).Value
	}
	d.out <- r
}

// Decoder is a VideoToolbox hardware decoder. The session opens on the
// first access unit that carries the stream's parameters, and reopens when
// they change.
type Decoder struct {
	cfg     decode.Config
	ref     uintptr
	record  *[2]uintptr
	session uintptr
	format  uintptr // CMVideoFormatDescriptionRef
	hevc    hevcParams
	av1Seq  []byte
	tenBit  bool
	out     chan decoded
	sample  []byte
}

// Open makes a decoder for cfg.Codec, HEVC or AV1.
func Open(cfg decode.Config) (*Decoder, error) {
	if cfg.Codec != encode.HEVC && cfg.Codec != encode.AV1 {
		return nil, fmt.Errorf("%w: %s", decode.ErrUnsupported, cfg.Codec)
	}
	cbOnce.Do(func() { callback = purego.NewCallback(onDecoded) })
	d := &Decoder{cfg: cfg, record: new([2]uintptr), out: make(chan decoded, 4)}
	decodersMu.Lock()
	nextRef++
	d.ref = nextRef
	decoders[d.ref] = d
	decodersMu.Unlock()
	d.record[0], d.record[1] = callback, d.ref
	return d, nil
}

// Decode decodes one access unit synchronously.
func (d *Decoder) Decode(au []byte, pts int64) (decode.Frame, error) {
	start := apple.Now()
	var data []byte
	var err error
	if d.cfg.Codec == encode.HEVC {
		var p hevcParams
		p, data, err = hevcSample(au, d.sample)
		if err == nil && p.complete() && !p.equal(d.hevc) {
			err = d.reformatHEVC(p)
		}
	} else {
		var seq obu
		seq, data, err = av1Sample(au, d.sample)
		if err == nil && seq.raw != nil && string(seq.raw) != string(d.av1Seq) {
			err = d.reformatAV1(seq)
		}
	}
	if err != nil {
		return decode.Frame{}, err
	}
	d.sample = data[:0]
	if d.session == 0 {
		return decode.Frame{}, decode.ErrNoFrame // no parameters seen yet
	}
	sbuf, err := d.sampleBuffer(data, pts)
	if err != nil {
		return decode.Frame{}, err
	}
	var info uint32
	st := vtDecompressionSessionDecodeFrame(d.session, sbuf, 0, 0, &info)
	apple.CFRelease(sbuf)
	if st != 0 {
		return decode.Frame{}, fmt.Errorf("vt: VTDecompressionSessionDecodeFrame: %d", st)
	}
	select {
	case r := <-d.out:
		if r.status != 0 || r.image == 0 {
			return decode.Frame{}, fmt.Errorf("vt: decode callback status %d", r.status)
		}
		f := decode.Frame{
			Surface: cvPixelBufferGetIOSurface(r.image),
			Format:  capture.FormatNV12,
			Width:   int(cvPixelBufferGetWidth(r.image)),
			Height:  int(cvPixelBufferGetHeight(r.image)),
			PTS:     r.pts,
			Latency: time.Duration(r.at - start),
			Ref:     r.image,
		}
		if cvPixelBufferGetPixelFormatType(r.image) == pixelx420 {
			f.Format = capture.FormatP010
		}
		return f, nil
	case <-time.After(time.Second):
		return decode.Frame{}, errors.New("vt: no decode callback in 1 s")
	}
}

func (d *Decoder) sampleBuffer(data []byte, pts int64) (uintptr, error) {
	var bb uintptr
	if st := cmBlockBufferCreateWithMemoryBlock(0, 0, uint(len(data)), 0, 0, 0, uint(len(data)), 0, &bb); st != 0 {
		return 0, fmt.Errorf("vt: CMBlockBufferCreateWithMemoryBlock: %d", st)
	}
	defer apple.CFRelease(bb)
	if st := cmBlockBufferReplaceDataBytes(unsafe.Pointer(&data[0]), bb, 0, uint(len(data))); st != 0 {
		return 0, fmt.Errorf("vt: CMBlockBufferReplaceDataBytes: %d", st)
	}
	fps := int32(max(d.cfg.FPS, 1))
	// CMSampleTimingInfo: duration, presentation, decode time stamps.
	timing := [3]apple.CMTime{apple.Time(1, fps), apple.Time(pts, fps), {}}
	size := uint(len(data))
	var sbuf uintptr
	if st := cmSampleBufferCreateReady(0, bb, d.format, 1, 1, unsafe.Pointer(&timing[0]), 1, unsafe.Pointer(&size), &sbuf); st != 0 {
		return 0, fmt.Errorf("vt: CMSampleBufferCreateReady: %d", st)
	}
	return sbuf, nil
}

func (d *Decoder) reformatHEVC(p hevcParams) error {
	sets := make([]unsafe.Pointer, len(p.sets))
	sizes := make([]uint, len(p.sets))
	for i, s := range p.sets {
		sets[i], sizes[i] = unsafe.Pointer(&s[0]), uint(len(s))
	}
	var f uintptr
	if st := cmVideoFormatDescriptionCreateFromHEVCParameterSets(0, uint(len(sets)), unsafe.Pointer(&sets[0]), unsafe.Pointer(&sizes[0]), 4, 0, &f); st != 0 {
		return fmt.Errorf("vt: HEVC format description: %d", st)
	}
	d.hevc = p.clone()
	return d.reopen(f, p.tenBit())
}

func (d *Decoder) reformatAV1(seq obu) error {
	s, err := parseAV1Sequence(seq.payload)
	if err != nil {
		return err
	}
	c := av1C(s, seq.raw)
	data := cfDataCreate(0, unsafe.Pointer(&c[0]), len(c))
	name := apple.String("av1C")
	atoms := apple.Dict(name, data)
	ext := apple.Dict(keyAtoms, atoms)
	defer func() {
		for _, r := range []uintptr{data, name, atoms, ext} {
			apple.CFRelease(r)
		}
	}()
	var f uintptr
	if st := cmVideoFormatDescriptionCreate(0, codecAV1, int32(s.width), int32(s.height), ext, &f); st != 0 {
		return fmt.Errorf("vt: AV1 format description: %d", st)
	}
	d.av1Seq = clone(seq.raw)
	return d.reopen(f, s.highBitDepth)
}

// cMemory views an address VideoToolbox passed in as a pointer; the memory
// is not Go's, so the garbage collector has nothing to track.
func cMemory(p uintptr) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(&p)) }

func clone(b []byte) []byte { return append([]byte(nil), b...) }

// reopen takes format as the stream's description, keeping the session
// when it accepts it.
func (d *Decoder) reopen(format uintptr, tenBit bool) error {
	if d.format != 0 {
		apple.CFRelease(d.format)
	}
	d.format = format
	if d.session != 0 && tenBit == d.tenBit && vtDecompressionSessionCanAcceptFormatDescription(d.session, format) {
		return nil
	}
	d.closeSession()
	d.tenBit = tenBit
	pixel := int64(pixel420v)
	if tenBit {
		pixel = pixelx420
	}
	surface := apple.Dict()
	pf := apple.Int(pixel)
	attrs := apple.Dict(keyIOSurface, surface, keyPixelFormat, pf)
	defer func() {
		for _, r := range []uintptr{surface, pf, attrs} {
			apple.CFRelease(r)
		}
	}()
	if st := vtDecompressionSessionCreate(0, format, 0, attrs, unsafe.Pointer(d.record), &d.session); st != 0 {
		d.session = 0
		return fmt.Errorf("%w: VTDecompressionSessionCreate: %d", decode.ErrUnsupported, st)
	}
	return nil
}

func (d *Decoder) closeSession() {
	if d.session != 0 {
		vtDecompressionSessionInvalidate(d.session)
		apple.CFRelease(d.session)
		d.session = 0
	}
}

// Release gives the picture's pixel buffer back to VideoToolbox's pool.
func (d *Decoder) Release(f decode.Frame) {
	if f.Ref != 0 {
		apple.CFRelease(f.Ref)
	}
}

func (d *Decoder) Close() error {
	d.closeSession()
	if d.format != 0 {
		apple.CFRelease(d.format)
		d.format = 0
	}
	decodersMu.Lock()
	delete(decoders, d.ref)
	decodersMu.Unlock()
	return nil
}

var _ decode.Decoder = (*Decoder)(nil)
