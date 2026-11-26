//go:build linux && !android && (amd64 || arm64)

package pipewire

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"

	"github.com/planckdotgg/planck/internal/clock"
	"github.com/planckdotgg/planck/pkg/capture"
)

// Offsets in libpipewire-0.3 1.0's structs on LP64, from offsetof in a C
// program built against its headers.
const (
	sizeStreamEvents = 96
	evVersion        = 0
	evStateChanged   = 16
	evParamChanged   = 40
	evProcess        = 64
	streamEventsV    = 2
	sizeHook         = 48

	loopControl    = 16 // struct pw_loop.control
	ifaceCallbacks = 16 // struct spa_interface.cb
	controlEnter   = 24 // struct spa_loop_control_methods
	controlLeave   = 32
	controlIterate = 40

	spaBufferNDatas = 4
	spaBufferDatas  = 16
	sizeSpaData     = 40
	dataType        = 0
	dataFD          = 8
	dataPtr         = 24
	dataChunk       = 32
	chunkOffset     = 0
	chunkStride     = 8
	chunkFlags      = 12
	chunkCorrupted  = 1

	directionInput  = 0
	flagAutoconnect = 1
	flagMapBuffers  = 4
	stateError      = -1
)

var lib struct {
	once sync.Once
	err  error

	init, loopNew, loopDestroy, contextNew, contextDestroy, connectFD, coreDisconnect uintptr
	propertiesNewString, streamNew, streamAddListener, streamConnect, streamDestroy   uintptr
	streamDequeue, streamQueue, streamUpdateParams                                    uintptr

	onState, onParam, onProcess uintptr
}

func load() error {
	lib.once.Do(func() {
		h, err := purego.Dlopen("libpipewire-0.3.so.0", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			lib.err = fmt.Errorf("pipewire: no libpipewire-0.3 (%v)", err)
			return
		}
		for _, f := range []struct {
			p    *uintptr
			name string
		}{
			{&lib.init, "pw_init"},
			{&lib.loopNew, "pw_loop_new"},
			{&lib.loopDestroy, "pw_loop_destroy"},
			{&lib.contextNew, "pw_context_new"},
			{&lib.contextDestroy, "pw_context_destroy"},
			{&lib.connectFD, "pw_context_connect_fd"},
			{&lib.coreDisconnect, "pw_core_disconnect"},
			{&lib.propertiesNewString, "pw_properties_new_string"},
			{&lib.streamNew, "pw_stream_new"},
			{&lib.streamAddListener, "pw_stream_add_listener"},
			{&lib.streamConnect, "pw_stream_connect"},
			{&lib.streamDestroy, "pw_stream_destroy"},
			{&lib.streamDequeue, "pw_stream_dequeue_buffer"},
			{&lib.streamQueue, "pw_stream_queue_buffer"},
			{&lib.streamUpdateParams, "pw_stream_update_params"},
		} {
			if *f.p, err = purego.Dlsym(h, f.name); err != nil {
				lib.err = fmt.Errorf("pipewire: %v", err)
				return
			}
		}
		call(lib.init, 0, 0)
		lib.onState = purego.NewCallback(onState)
		lib.onParam = purego.NewCallback(onParam)
		lib.onProcess = purego.NewCallback(onProcess)
	})
	return lib.err
}

// call calls C; go:uintptrescapes keeps a pointer argument valid when the
// goroutine's stack moves before the call.
//
//go:uintptrescapes
func call(fn uintptr, args ...uintptr) uintptr {
	r, _, _ := purego.SyscallN(fn, args...)
	return r
}

func ptr(a uintptr) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(&a)) }

func field[T any](base, off uintptr) T { return *(*T)(ptr(base + off)) }

func cString(p uintptr) string {
	if p == 0 {
		return ""
	}
	n := 0
	for *(*byte)(ptr(p + uintptr(n))) != 0 {
		n++
	}
	return string(unsafe.Slice((*byte)(ptr(p)), n))
}

// streams finds a Source from the id PipeWire hands back to callbacks.
var (
	streams  sync.Map
	streamID atomic.Uintptr
)

func sourceOf(data uintptr) *Source {
	s, _ := streams.Load(data)
	src, _ := s.(*Source)
	return src
}

// frame is a buffer the stream gave out, with what it holds.
type frame struct {
	pw  uintptr
	buf *capture.DMABuf
	at  time.Duration
}

// Source captures the monitor a ScreenCast portal session streams.
type Source struct {
	cast    *castSession
	display capture.Display
	id      uintptr

	// The loop goroutine's, which every PipeWire call and callback runs on.
	stream  uintptr
	events  [sizeStreamEvents]byte
	hook    [sizeHook]byte
	params  [][]byte
	format  videoFormat
	serials map[uintptr]uint64
	descs   map[uintptr]*capture.DMABuf
	serial  uint64
	failed  error
	ready   chan error

	frames  chan frame
	release chan uintptr
	quit    atomic.Bool
	done    chan struct{}
	held    *frame
}

// Open starts a ScreenCast session (the portal asks the user for a
// monitor unless a restore token answers for them) and streams it, at
// fps by default.
func Open(fps int) (*Source, error) {
	if err := load(); err != nil {
		return nil, err
	}
	cast, err := startCast()
	if err != nil {
		return nil, err
	}
	s := &Source{cast: cast, id: streamID.Add(1), ready: make(chan error, 1), frames: make(chan frame, 1),
		release: make(chan uintptr, 8), done: make(chan struct{}), serials: map[uintptr]uint64{}, descs: map[uintptr]*capture.DMABuf{}}
	w, h := max(cast.w, 1920), max(cast.h, 1080)
	if cast.w > 0 && cast.h > 0 {
		w, h = cast.w, cast.h
	}
	s.params = [][]byte{enumFormat(uint32(w), uint32(h), uint32(max(fps, 1)), []int64{int64(capture.ModifierLinear)}),
		enumFormat(uint32(w), uint32(h), uint32(max(fps, 1)), nil)}
	streams.Store(s.id, s)
	go s.loop()
	select {
	case err = <-s.ready:
	case <-time.After(10 * time.Second):
		err = errors.New("pipewire: the stream negotiated no format in 10 s")
	}
	if err != nil {
		s.Close()
		return nil, err
	}
	s.display = capture.Display{ID: "portal", Name: "Portal screen", X: cast.x, Y: cast.y,
		Width: int(s.format.width), Height: int(s.format.height), Refresh: float64(max(fps, 60)), Primary: true}
	return s, nil
}

// Display is the monitor the portal streams.
func (s *Source) Display() capture.Display { return s.display }

// loop owns the PipeWire loop on its own OS thread, which it never
// unlocks: PipeWire calls back on the thread that iterates.
func (s *Source) loop() {
	runtime.LockOSThread()
	defer close(s.done)
	fail := func(err error) {
		select {
		case s.ready <- err:
		default:
		}
	}
	loop := call(lib.loopNew, 0)
	if loop == 0 {
		fail(errors.New("pipewire: pw_loop_new failed"))
		return
	}
	defer call(lib.loopDestroy, loop)
	ctx := call(lib.contextNew, loop, 0, 0)
	if ctx == 0 {
		fail(errors.New("pipewire: pw_context_new failed"))
		return
	}
	defer call(lib.contextDestroy, ctx)
	core := call(lib.connectFD, ctx, uintptr(s.cast.fd), 0, 0)
	s.cast.fd = -1 // the core owns it now
	if core == 0 {
		fail(errors.New("pipewire: cannot connect to the portal's PipeWire remote"))
		return
	}
	defer call(lib.coreDisconnect, core)
	props := []byte("media.type=Video media.category=Capture media.role=Screen\x00")
	name := []byte("planck-capture\x00")
	s.stream = call(lib.streamNew, core, uintptr(unsafe.Pointer(&name[0])),
		call(lib.propertiesNewString, uintptr(unsafe.Pointer(&props[0]))))
	if s.stream == 0 {
		fail(errors.New("pipewire: pw_stream_new failed"))
		return
	}
	defer call(lib.streamDestroy, s.stream)
	*(*uint32)(unsafe.Pointer(&s.events[evVersion])) = streamEventsV
	*(*uintptr)(unsafe.Pointer(&s.events[evStateChanged])) = lib.onState
	*(*uintptr)(unsafe.Pointer(&s.events[evParamChanged])) = lib.onParam
	*(*uintptr)(unsafe.Pointer(&s.events[evProcess])) = lib.onProcess
	call(lib.streamAddListener, s.stream, uintptr(unsafe.Pointer(&s.hook[0])), uintptr(unsafe.Pointer(&s.events[0])), s.id)
	pods := make([]uintptr, len(s.params))
	for i, p := range s.params {
		pods[i] = uintptr(unsafe.Pointer(&p[0]))
	}
	if r := int32(call(lib.streamConnect, s.stream, directionInput, uintptr(s.cast.node), flagAutoconnect|flagMapBuffers,
		uintptr(unsafe.Pointer(&pods[0])), uintptr(len(pods)))); r < 0 {
		fail(fmt.Errorf("pipewire: pw_stream_connect: %d", r))
		return
	}
	runtime.KeepAlive(s.params)
	ctl := field[uintptr](loop, loopControl)
	funcs, data := field[uintptr](ctl, ifaceCallbacks), field[uintptr](ctl, ifaceCallbacks+8)
	call(field[uintptr](funcs, controlEnter), data)
	defer call(field[uintptr](funcs, controlLeave), data)
	iterate := field[uintptr](funcs, controlIterate)
	for !s.quit.Load() {
		call(iterate, data, 5)
		for drained := false; !drained; {
			select {
			case b := <-s.release:
				call(lib.streamQueue, s.stream, b)
			default:
				drained = true
			}
		}
	}
	// Give back what the consumer has not taken, then stop.
	for drained := false; !drained; {
		select {
		case f := <-s.frames:
			call(lib.streamQueue, s.stream, f.pw)
		default:
			drained = true
		}
	}
}

func onState(data, old, state, errMsg uintptr) uintptr {
	if s := sourceOf(data); s != nil && int32(state) == stateError {
		s.failed = fmt.Errorf("pipewire: stream error: %s", cString(errMsg))
		select {
		case s.ready <- s.failed:
		default:
		}
	}
	return 0
}

// onParam takes the negotiated format and asks for buffers to suit it:
// DMA-BUFs with a modifier, shared memory without.
func onParam(data, id, param uintptr) uintptr {
	s := sourceOf(data)
	if s == nil || uint32(id) != paramFormat || param == 0 {
		return 0
	}
	b := unsafe.Slice((*byte)(ptr(param)), 8+int(field[uint32](param, 0)))
	f, err := parseFormat(b)
	if err != nil {
		return 0
	}
	if _, ok := drmFourcc(f.format); !ok {
		return 0
	}
	s.format = f
	clear(s.serials)
	clear(s.descs)
	types := int32(1<<dataMemFd | 1<<dataMemPtr)
	if f.hasModifier {
		types = 1 << dataDmaBuf
	}
	p := buffersParam(types)
	pods := []uintptr{uintptr(unsafe.Pointer(&p[0]))}
	call(lib.streamUpdateParams, s.stream, uintptr(unsafe.Pointer(&pods[0])), 1)
	runtime.KeepAlive(p)
	select {
	case s.ready <- nil:
	default:
	}
	return 0
}

func drmFourcc(f uint32) (uint32, bool) {
	switch f {
	case videoBGRx:
		return capture.FourccXRGB8888, true
	case videoRGBx:
		return capture.FourccXBGR8888, true
	case videoBGRA:
		return capture.FourccARGB8888, true
	case videoRGBA:
		return capture.FourccABGR8888, true
	}
	return 0, false
}

// onProcess takes the newest buffer, gives back older ones and any the
// consumer has not taken yet, and offers the newest to Next.
func onProcess(data uintptr) uintptr {
	s := sourceOf(data)
	if s == nil {
		return 0
	}
	var newest uintptr
	for {
		b := call(lib.streamDequeue, s.stream)
		if b == 0 {
			break
		}
		if newest != 0 {
			call(lib.streamQueue, s.stream, newest)
		}
		newest = b
	}
	if newest == 0 {
		return 0
	}
	d := s.describe(newest)
	if d == nil {
		call(lib.streamQueue, s.stream, newest)
		return 0
	}
	select {
	case old := <-s.frames:
		call(lib.streamQueue, s.stream, old.pw)
	default:
	}
	s.frames <- frame{pw: newest, buf: d, at: clock.Now()}
	return 0
}

// describe makes the DMABuf of a pw_buffer: its DMA-BUF planes, or its
// planes in mapped memory. A corrupted or empty buffer has none.
func (s *Source) describe(pw uintptr) *capture.DMABuf {
	spa := field[uintptr](pw, 0)
	n := int(field[uint32](spa, spaBufferNDatas))
	datas := field[uintptr](spa, spaBufferDatas)
	fourcc, _ := drmFourcc(s.format.format)
	if n == 0 || n > 4 {
		return nil
	}
	d := s.descs[pw]
	if d == nil {
		d = &capture.DMABuf{}
		s.descs[pw] = d
	}
	serial, ok := s.serials[pw]
	if !ok {
		s.serial++
		serial = s.serial
		s.serials[pw] = serial
	}
	*d = capture.DMABuf{Fourcc: fourcc, Modifier: uint64(s.format.modifier), Width: int(s.format.width), Height: int(s.format.height),
		Planes: n, Serial: serial}
	if !s.format.hasModifier {
		d.Modifier = capture.ModifierLinear
	}
	for i := range n {
		sd := datas + uintptr(i)*sizeSpaData
		chunk := field[uintptr](sd, dataChunk)
		if chunk == 0 || field[uint32](chunk, chunkFlags)&chunkCorrupted != 0 {
			return nil
		}
		d.Offsets[i] = field[uint32](chunk, chunkOffset)
		d.Pitches[i] = uint32(field[int32](chunk, chunkStride))
		switch field[uint32](sd, dataType) {
		case dataDmaBuf:
			d.FDs[i] = int32(field[int64](sd, dataFD))
		case dataMemFd, dataMemPtr:
			p := field[uintptr](sd, dataPtr)
			if p == 0 {
				return nil
			}
			d.CPU[i] = ptr(p + uintptr(d.Offsets[i]))
			d.Offsets[i] = 0
		default:
			return nil
		}
	}
	return d
}

// Next waits for the compositor's next picture: Wayland sends one when
// the screen changed, so a still screen times out.
func (s *Source) Next(timeout time.Duration) (capture.Frame, error) {
	if s.held != nil {
		s.Release()
	}
	select {
	case f := <-s.frames:
		s.held = &f
		return capture.Frame{Surface: f.buf.Handle(), Format: capture.FormatBGRA8, Width: f.buf.Width, Height: f.buf.Height,
			Presented: f.at, Acquired: clock.Now()}, nil
	case <-s.done:
		if s.failed != nil {
			return capture.Frame{}, fmt.Errorf("%w: %v", capture.ErrAccessLost, s.failed)
		}
		return capture.Frame{}, capture.ErrAccessLost
	case <-time.After(timeout):
		return capture.Frame{}, capture.ErrTimeout
	}
}

func (s *Source) Release() error {
	if s.held != nil {
		s.release <- s.held.pw
		s.held = nil
	}
	return nil
}

func (s *Source) Close() error {
	if s.cast == nil {
		return nil
	}
	s.held = nil
	s.quit.Store(true)
	select {
	case <-s.done:
	case <-time.After(2 * time.Second):
	}
	streams.Delete(s.id)
	if s.cast.fd >= 0 {
		syscall.Close(s.cast.fd)
	}
	s.cast.close()
	s.cast = nil
	return nil
}

var _ capture.Source = (*Source)(nil)
