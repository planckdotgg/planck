package nvdec

import (
	"fmt"
	"runtime"
	"sync"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"

	"github.com/planckdotgg/planck/pkg/capture"
	"github.com/planckdotgg/planck/pkg/decode"
	"github.com/planckdotgg/planck/pkg/decode/internal/hostbuf"
	"github.com/planckdotgg/planck/pkg/encode"
)

// Struct sizes and offsets on linux/amd64 and arm64, from FFmpeg's
// nv-codec-headers 13.0 (dynlink_nvcuvid.h, dynlink_cuviddec.h), checked
// with offsetof under gcc.
const (
	parserParamsSize = 136
	ppMaxSurfaces    = 4
	ppErrorThreshold = 12
	ppMaxDelay       = 16
	ppUserData       = 40
	ppSequence       = 48
	ppDecode         = 56
	ppDisplay        = 64

	fmtCodec        = 0
	fmtBitDepth     = 13
	fmtMinSurfaces  = 15
	fmtCodedWidth   = 16
	fmtCodedHeight  = 20
	fmtDisplayArea  = 24
	fmtChromaFormat = 40

	createInfoSize   = 176
	ciWidth          = 0
	ciHeight         = 8
	ciNumSurfaces    = 16
	ciCodec          = 24
	ciChroma         = 28
	ciFlags          = 32
	ciBitDepth       = 40
	ciMaxWidth       = 56
	ciMaxHeight      = 64
	ciDisplayArea    = 80
	ciOutputFormat   = 88
	ciTargetWidth    = 96
	ciTargetHeight   = 104
	ciOutputSurfaces = 112
	ciLock           = 120

	procParamsSize = 264
	memcpy2DSize   = 128

	cudaVideoCodecHEVC = 8
	cudaVideoCodecAV1  = 11

	surfaceNV12 = 0
	surfaceP016 = 1

	createPreferCUVID = 4

	pktEndOfPicture = 8
	pktTimestamp    = 2

	memoryTypeHost   = 1
	memoryTypeDevice = 2
)

var lib struct {
	once sync.Once
	err  error

	cuInit, cuDeviceGetCount, cuDeviceGet, cuDeviceGetName, cuCtxCreate, cuCtxDestroy uintptr
	cuCtxPush, cuCtxPop, cuMemcpy2D, cuMemHostRegister, cuMemHostUnregister           uintptr

	createParser, parse, destroyParser, createDecoder, destroyDecoder, decodePicture uintptr
	mapFrame, unmapFrame, lockCreate, lockDestroy                                    uintptr

	onSequence, onDecode, onDisplay uintptr
}

func load() error {
	lib.once.Do(func() {
		cuda, err := purego.Dlopen("libcuda.so.1", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			lib.err = fmt.Errorf("%w: no NVIDIA driver (%v)", decode.ErrUnsupported, err)
			return
		}
		cuvid, err := purego.Dlopen("libnvcuvid.so.1", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			lib.err = fmt.Errorf("%w: no NVDEC library (%v)", decode.ErrUnsupported, err)
			return
		}
		for _, f := range []struct {
			h    uintptr
			p    *uintptr
			name string
		}{
			{cuda, &lib.cuInit, "cuInit"},
			{cuda, &lib.cuDeviceGetCount, "cuDeviceGetCount"},
			{cuda, &lib.cuDeviceGet, "cuDeviceGet"},
			{cuda, &lib.cuDeviceGetName, "cuDeviceGetName"},
			{cuda, &lib.cuCtxCreate, "cuCtxCreate_v2"},
			{cuda, &lib.cuCtxDestroy, "cuCtxDestroy_v2"},
			{cuda, &lib.cuCtxPush, "cuCtxPushCurrent_v2"},
			{cuda, &lib.cuCtxPop, "cuCtxPopCurrent_v2"},
			{cuda, &lib.cuMemcpy2D, "cuMemcpy2D_v2"},
			{cuda, &lib.cuMemHostRegister, "cuMemHostRegister_v2"},
			{cuda, &lib.cuMemHostUnregister, "cuMemHostUnregister"},
			{cuvid, &lib.createParser, "cuvidCreateVideoParser"},
			{cuvid, &lib.parse, "cuvidParseVideoData"},
			{cuvid, &lib.destroyParser, "cuvidDestroyVideoParser"},
			{cuvid, &lib.createDecoder, "cuvidCreateDecoder"},
			{cuvid, &lib.destroyDecoder, "cuvidDestroyDecoder"},
			{cuvid, &lib.decodePicture, "cuvidDecodePicture"},
			{cuvid, &lib.mapFrame, "cuvidMapVideoFrame64"},
			{cuvid, &lib.unmapFrame, "cuvidUnmapVideoFrame64"},
			{cuvid, &lib.lockCreate, "cuvidCtxLockCreate"},
			{cuvid, &lib.lockDestroy, "cuvidCtxLockDestroy"},
		} {
			if *f.p, err = purego.Dlsym(f.h, f.name); err != nil {
				lib.err = fmt.Errorf("%w: %v", decode.ErrUnsupported, err)
				return
			}
		}
		if r := call(lib.cuInit, 0); r != 0 {
			lib.err = fmt.Errorf("%w: cuInit: CUDA error %d", decode.ErrUnsupported, r)
			return
		}
		lib.onSequence = purego.NewCallback(onSequence)
		lib.onDecode = purego.NewCallback(onDecode)
		lib.onDisplay = purego.NewCallback(onDisplay)
	})
	return lib.err
}

// call calls C; go:uintptrescapes keeps a pointer argument valid when the
// goroutine's stack moves before the call.
//
//go:uintptrescapes
func call(fn uintptr, args ...uintptr) uintptr {
	r, _, _ := purego.SyscallN(fn, args...)
	return uintptr(int32(r))
}

func ptr(a uintptr) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(&a)) }

func field[T any](base uintptr, off uintptr) T { return *(*T)(ptr(base + off)) }

func set[T any](b []byte, off int, v T) { *(*T)(unsafe.Pointer(&b[off])) = v }

// Available reports whether NVDEC loads, with an error saying why not.
func Available() error {
	if err := load(); err != nil {
		return err
	}
	var n int32
	if r := call(lib.cuDeviceGetCount, uintptr(unsafe.Pointer(&n))); r != 0 || n == 0 {
		return fmt.Errorf("%w: no CUDA device", decode.ErrUnsupported)
	}
	return nil
}

// Decoder is an NVDEC decoder of one stream.
type Decoder struct {
	id     uintptr
	codec  encode.Codec
	ctx    uintptr
	lock   uintptr
	parser uintptr
	dec    uintptr

	// The sequence's: picture size, depth and NVDEC's surfaces.
	width, height  int
	codedW, codedH uint32
	tenBit         bool
	surfaces       int
	created        bool
	pool           *hostbuf.Pool
	pools          []*hostbuf.Pool

	// shown are the pictures the parser gave out during a parse, in
	// display order; err the first callback failure.
	shown []int32
	err   error

	// C-side arguments, on the heap so a stack move cannot stale them.
	packet [32]byte
	proc   [procParamsSize]byte
	copy2D [memcpy2DSize]byte
	devPtr uint64
	pitch  uint32
}

var (
	decodersMu sync.Mutex
	decoders   = map[uintptr]*Decoder{}
	nextID     uintptr
)

// Open makes a decoder for cfg.Codec, HEVC or AV1, on the first CUDA
// device.
func Open(cfg decode.Config) (*Decoder, error) {
	var codec uint32
	switch cfg.Codec {
	case encode.HEVC:
		codec = cudaVideoCodecHEVC
	case encode.AV1:
		codec = cudaVideoCodecAV1
	default:
		return nil, fmt.Errorf("%w: %s", decode.ErrUnsupported, cfg.Codec)
	}
	if err := Available(); err != nil {
		return nil, err
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	d := &Decoder{codec: cfg.Codec}
	var dev int32
	if r := call(lib.cuDeviceGet, uintptr(unsafe.Pointer(&dev)), 0); r != 0 {
		return nil, fmt.Errorf("nvdec: cuDeviceGet: CUDA error %d", r)
	}
	if r := call(lib.cuCtxCreate, uintptr(unsafe.Pointer(&d.ctx)), 0, uintptr(dev)); r != 0 {
		return nil, fmt.Errorf("nvdec: cuCtxCreate: CUDA error %d", r)
	}
	var popped uintptr
	call(lib.cuCtxPop, uintptr(unsafe.Pointer(&popped)))
	if r := call(lib.lockCreate, uintptr(unsafe.Pointer(&d.lock)), d.ctx); r != 0 {
		d.Close()
		return nil, fmt.Errorf("nvdec: cuvidCtxLockCreate: CUDA error %d", r)
	}
	decodersMu.Lock()
	nextID++
	d.id = nextID
	decoders[d.id] = d
	decodersMu.Unlock()
	params := make([]byte, parserParamsSize)
	set(params, 0, codec)
	set(params, ppMaxSurfaces, uint32(1))
	set(params, ppErrorThreshold, uint32(100))
	set(params, ppMaxDelay, uint32(0))
	set(params, ppUserData, d.id)
	set(params, ppSequence, lib.onSequence)
	set(params, ppDecode, lib.onDecode)
	set(params, ppDisplay, lib.onDisplay)
	if r := call(lib.createParser, uintptr(unsafe.Pointer(&d.parser)), uintptr(unsafe.Pointer(&params[0]))); r != 0 {
		d.Close()
		return nil, fmt.Errorf("nvdec: cuvidCreateVideoParser: CUDA error %d", r)
	}
	return d, nil
}

func decoderOf(user uintptr) *Decoder {
	decodersMu.Lock()
	defer decodersMu.Unlock()
	return decoders[user]
}

// onSequence makes the decoder for a new sequence, or keeps it for one of
// the same size and depth. It returns the surfaces the parser may use.
func onSequence(user, format uintptr) uintptr {
	d := decoderOf(user)
	if d == nil {
		return 0
	}
	codedW, codedH := field[uint32](format, fmtCodedWidth), field[uint32](format, fmtCodedHeight)
	left, top := field[int32](format, fmtDisplayArea), field[int32](format, fmtDisplayArea+4)
	right, bottom := field[int32](format, fmtDisplayArea+8), field[int32](format, fmtDisplayArea+12)
	tenBit := field[uint8](format, fmtBitDepth) > 0
	width, height := int(right-left), int(bottom-top)
	surfaces := int(field[uint8](format, fmtMinSurfaces)) + 3
	if d.created && codedW == d.codedW && codedH == d.codedH && tenBit == d.tenBit && width == d.width && height == d.height {
		return uintptr(d.surfaces)
	}
	if d.dec != 0 {
		call(lib.destroyDecoder, d.dec)
		d.dec, d.created = 0, false
	}
	info := make([]byte, createInfoSize)
	set(info, ciWidth, uint64(codedW))
	set(info, ciHeight, uint64(codedH))
	set(info, ciNumSurfaces, uint64(surfaces))
	set(info, ciCodec, field[uint32](format, fmtCodec))
	set(info, ciChroma, field[uint32](format, fmtChromaFormat))
	set(info, ciFlags, uint64(createPreferCUVID))
	set(info, ciBitDepth, uint64(field[uint8](format, fmtBitDepth)))
	set(info, ciMaxWidth, uint64(codedW))
	set(info, ciMaxHeight, uint64(codedH))
	set(info, ciDisplayArea, int16(left))
	set(info, ciDisplayArea+2, int16(top))
	set(info, ciDisplayArea+4, int16(right))
	set(info, ciDisplayArea+6, int16(bottom))
	out := uint32(surfaceNV12)
	if tenBit {
		out = surfaceP016
	}
	set(info, ciOutputFormat, out)
	set(info, ciTargetWidth, uint64(width))
	set(info, ciTargetHeight, uint64(height))
	set(info, ciOutputSurfaces, uint64(2))
	set(info, ciLock, d.lock)
	if r := call(lib.createDecoder, uintptr(unsafe.Pointer(&d.dec)), uintptr(unsafe.Pointer(&info[0]))); r != 0 {
		d.err = fmt.Errorf("nvdec: cuvidCreateDecoder %dx%d (10-bit %v): CUDA error %d", codedW, codedH, tenBit, r)
		return 0
	}
	d.codedW, d.codedH, d.tenBit, d.width, d.height, d.surfaces, d.created = codedW, codedH, tenBit, width, height, surfaces, true
	fourcc := capture.FourccNV12
	if tenBit {
		fourcc = capture.FourccP010
	}
	if !d.pool.Matches(fourcc, width, height) {
		// The old pool's buffers may still be out: Close frees them all.
		d.pool = hostbuf.New(fourcc, width, height, 8)
		d.pools = append(d.pools, d.pool)
	}
	return uintptr(surfaces)
}

func onDecode(user, pic uintptr) uintptr {
	d := decoderOf(user)
	if d == nil || d.dec == 0 {
		return 0
	}
	if r := call(lib.decodePicture, d.dec, pic); r != 0 {
		d.err = fmt.Errorf("nvdec: cuvidDecodePicture: CUDA error %d", r)
		return 0
	}
	return 1
}

func onDisplay(user, info uintptr) uintptr {
	d := decoderOf(user)
	if d == nil {
		return 0
	}
	if info != 0 {
		d.shown = append(d.shown, field[int32](info, 0))
	}
	return 1
}

// Decode parses one access unit and returns its picture, copied into
// memory.
func (d *Decoder) Decode(au []byte, pts int64) (decode.Frame, error) {
	start := time.Now()
	if len(au) == 0 {
		return decode.Frame{}, decode.ErrNoFrame
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	call(lib.cuCtxPush, d.ctx)
	defer func() {
		var popped uintptr
		call(lib.cuCtxPop, uintptr(unsafe.Pointer(&popped)))
	}()
	d.shown, d.err = d.shown[:0], nil
	set(d.packet[:], 0, uint64(pktEndOfPicture|pktTimestamp))
	set(d.packet[:], 8, uint64(len(au)))
	set(d.packet[:], 16, uintptr(unsafe.Pointer(&au[0])))
	set(d.packet[:], 24, pts)
	r := call(lib.parse, d.parser, uintptr(unsafe.Pointer(&d.packet[0])))
	runtime.KeepAlive(au)
	if d.err != nil {
		return decode.Frame{}, d.err
	}
	if r != 0 {
		return decode.Frame{}, fmt.Errorf("nvdec: cuvidParseVideoData: CUDA error %d", r)
	}
	if len(d.shown) == 0 {
		return decode.Frame{}, decode.ErrNoFrame
	}
	// Show the newest; with no display delay there is one.
	b, err := d.download(d.shown[len(d.shown)-1])
	if err != nil {
		return decode.Frame{}, err
	}
	b.DMABuf.Width, b.DMABuf.Height = d.width, d.height
	format := capture.FormatNV12
	if d.tenBit {
		format = capture.FormatP010
	}
	return decode.Frame{
		Surface: b.DMABuf.Handle(), Format: format, Width: d.width, Height: d.height,
		PTS: pts, Latency: time.Since(start), Ref: uintptr(b.Index()) + 1,
	}, nil
}

// download maps picture index idx and copies its planes into a buffer of
// the pool.
func (d *Decoder) download(idx int32) (*hostbuf.Buffer, error) {
	b, err := d.pool.Get()
	if err != nil {
		return nil, err
	}
	clear(d.proc[:])
	set(d.proc[:], 0, int32(1)) // progressive_frame
	if r := call(lib.mapFrame, d.dec, uintptr(idx), uintptr(unsafe.Pointer(&d.devPtr)), uintptr(unsafe.Pointer(&d.pitch)), uintptr(unsafe.Pointer(&d.proc[0]))); r != 0 {
		d.pool.Put(b)
		return nil, fmt.Errorf("nvdec: cuvidMapVideoFrame: CUDA error %d", r)
	}
	bytes := 1
	if d.tenBit {
		bytes = 2
	}
	pitch := uint64(d.pitch)
	err = d.copyPlane(d.devPtr, pitch, b.Luma, b.Pitch, d.width*bytes, d.height)
	if err == nil {
		err = d.copyPlane(d.devPtr+pitch*uint64(d.height), pitch, b.Chroma, b.Pitch, (d.width+1)/2*2*bytes, (d.height+1)/2)
	}
	call(lib.unmapFrame, d.dec, uintptr(d.devPtr))
	if err != nil {
		d.pool.Put(b)
		return nil, err
	}
	return b, nil
}

func (d *Decoder) copyPlane(src, srcPitch uint64, dst []byte, dstPitch, widthBytes, rows int) error {
	c := d.copy2D[:]
	clear(c)
	set(c, 16, uint32(memoryTypeDevice))
	set(c, 32, src)
	set(c, 48, uint64(srcPitch))
	set(c, 72, uint32(memoryTypeHost))
	set(c, 80, uintptr(unsafe.Pointer(&dst[0])))
	set(c, 104, uint64(dstPitch))
	set(c, 112, uint64(widthBytes))
	set(c, 120, uint64(rows))
	if r := call(lib.cuMemcpy2D, uintptr(unsafe.Pointer(&c[0]))); r != 0 {
		return fmt.Errorf("nvdec: cuMemcpy2D: CUDA error %d", r)
	}
	return nil
}

// Release gives a picture's buffer back.
func (d *Decoder) Release(f decode.Frame) {
	if f.Ref == 0 || d.pool == nil {
		return
	}
	for _, p := range d.pools {
		if b := p.At(int(f.Ref) - 1); b != nil && b.DMABuf.Handle() == f.Surface {
			p.Put(b)
			return
		}
	}
}

// Close frees the decoder and its pictures, which must all be released.
func (d *Decoder) Close() error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if d.parser != 0 {
		call(lib.destroyParser, d.parser)
		d.parser = 0
	}
	if d.dec != 0 {
		call(lib.cuCtxPush, d.ctx)
		call(lib.destroyDecoder, d.dec)
		var popped uintptr
		call(lib.cuCtxPop, uintptr(unsafe.Pointer(&popped)))
		d.dec = 0
	}
	if d.lock != 0 {
		call(lib.lockDestroy, d.lock)
		d.lock = 0
	}
	if d.ctx != 0 {
		call(lib.cuCtxDestroy, d.ctx)
		d.ctx = 0
	}
	for _, p := range d.pools {
		p.Close()
	}
	d.pools, d.pool = nil, nil
	decodersMu.Lock()
	delete(decoders, d.id)
	decodersMu.Unlock()
	return nil
}

var _ decode.Decoder = (*Decoder)(nil)
