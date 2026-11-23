//go:build darwin

package audio

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"

	"github.com/watzon/planck/internal/apple"
)

var (
	sckLib = func() uintptr {
		h, err := purego.Dlopen("/System/Library/Frameworks/ScreenCaptureKit.framework/ScreenCaptureKit", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			panic(err)
		}
		return h
	}()

	dispatchQueueCreate                func(label string, attr uintptr) uintptr
	cmSampleBufferGetFormatDescription func(sbuf uintptr) uintptr
	cmAudioFormatGetStreamBasic        func(desc uintptr) unsafe.Pointer
	cmSampleBufferGetNumSamples        func(sbuf uintptr) int
	cmSampleBufferGetSampleTimingInfo  func(sbuf uintptr, index int, out unsafe.Pointer) int32
	cmSampleBufferGetAudioBufferList   func(sbuf uintptr, sizeNeeded, abl unsafe.Pointer, ablSize uintptr, alloc, memAlloc uintptr, flags uint32, block unsafe.Pointer) int32
	cgMainDisplayID                    func() uint32
)

func init() {
	apple.Register(&dispatchQueueCreate, apple.System, "dispatch_queue_create")
	apple.Register(&cmSampleBufferGetFormatDescription, apple.CoreMedia, "CMSampleBufferGetFormatDescription")
	apple.Register(&cmAudioFormatGetStreamBasic, apple.CoreMedia, "CMAudioFormatDescriptionGetStreamBasicDescription")
	apple.Register(&cmSampleBufferGetNumSamples, apple.CoreMedia, "CMSampleBufferGetNumSamples")
	apple.Register(&cmSampleBufferGetSampleTimingInfo, apple.CoreMedia, "CMSampleBufferGetSampleTimingInfo")
	apple.Register(&cmSampleBufferGetAudioBufferList, apple.CoreMedia, "CMSampleBufferGetAudioBufferListWithRetainedBlockBuffer")
	apple.Register(&cgMainDisplayID, apple.CoreGraphics, "CGMainDisplayID")
}

const (
	scStreamOutputScreen = 0
	scStreamOutputAudio  = 1

	audioFormatLinearPCM   = 0x6C70636D // 'lpcm'
	audioFlagIsFloat       = 1 << 0
	audioFlagNonInterleave = 1 << 5
	assure16ByteAlignment  = 1 << 0
)

type streamBasicDescription struct {
	sampleRate       float64
	formatID         uint32
	formatFlags      uint32
	bytesPerPacket   uint32
	framesPerPacket  uint32
	bytesPerFrame    uint32
	channelsPerFrame uint32
	bitsPerChannel   uint32
	reserved         uint32
}

type audioBuffer struct {
	channels uint32
	size     uint32
	data     unsafe.Pointer
}

type audioBufferList struct {
	count   uint32
	_       uint32
	buffers [8]audioBuffer
}

type sampleTimingInfo struct {
	duration, pts, dts apple.CMTime
}

// SCKCapturer records system audio with ScreenCaptureKit, excluding this
// process's own output. It needs the Screen Recording permission.
type SCKCapturer struct {
	mu       sync.Mutex
	stream   objc.ID
	output   objc.ID
	onPCM    func([]int16, int64)
	cancel   context.CancelFunc
	stopped  bool
	rejected atomic.Uint64

	// Used only on the capture queue.
	abl    audioBufferList
	timing sampleTimingInfo
	pcm    []int16
}

var (
	audioClassOnce sync.Once
	audioClassErr  error
	capturers      sync.Map // output ID -> *SCKCapturer
)

func NewCapturer() (*SCKCapturer, error) { return &SCKCapturer{}, nil }

func sel(s string) objc.SEL { return objc.RegisterName(s) }

func nsError(err objc.ID) error {
	if err == 0 {
		return nil
	}
	p := objc.Send[*byte](err.Send(sel("localizedDescription")), sel("UTF8String"))
	if p == nil {
		return errors.New("unknown error")
	}
	n := 0
	for *(*byte)(unsafe.Add(unsafe.Pointer(p), n)) != 0 {
		n++
	}
	return errors.New(string(unsafe.Slice(p, n)))
}

func registerAudioClass() {
	_, audioClassErr = objc.RegisterClass("PlanckAudioOutput", objc.GetClass("NSObject"),
		[]*objc.Protocol{objc.GetProtocol("SCStreamOutput")}, nil,
		[]objc.MethodDef{{Cmd: sel("stream:didOutputSampleBuffer:ofType:"), Fn: didOutputAudio}})
}

func didOutputAudio(self objc.ID, _ objc.SEL, _ objc.ID, sbuf uintptr, kind int) {
	if kind != scStreamOutputAudio {
		return
	}
	v, ok := capturers.Load(self)
	if !ok {
		return
	}
	v.(*SCKCapturer).deliver(sbuf)
}

// Rejected counts sample buffers dropped because their format or layout was
// not the 48 kHz float32 PCM the stream was configured for.
func (c *SCKCapturer) Rejected() uint64 { return c.rejected.Load() }

func (c *SCKCapturer) deliver(sbuf uintptr) {
	asbdPtr := cmAudioFormatGetStreamBasic(cmSampleBufferGetFormatDescription(sbuf))
	frames := cmSampleBufferGetNumSamples(sbuf)
	if asbdPtr == nil || frames <= 0 {
		c.rejected.Add(1)
		return
	}
	asbd := (*streamBasicDescription)(asbdPtr)
	if asbd.formatID != audioFormatLinearPCM || asbd.formatFlags&audioFlagIsFloat == 0 || asbd.bitsPerChannel != 32 ||
		asbd.sampleRate != SampleRate || asbd.channelsPerFrame == 0 {
		c.rejected.Add(1)
		return
	}
	// CoreMedia rejects any list size other than the exact one it needs.
	var size uintptr
	if cmSampleBufferGetAudioBufferList(sbuf, unsafe.Pointer(&size), nil, 0, 0, 0, 0, nil) != 0 || size > unsafe.Sizeof(c.abl) {
		c.rejected.Add(1)
		return
	}
	var block uintptr
	if cmSampleBufferGetAudioBufferList(sbuf, nil, unsafe.Pointer(&c.abl), size, 0, 0, assure16ByteAlignment, unsafe.Pointer(&block)) != 0 {
		c.rejected.Add(1)
		return
	}
	defer apple.CFRelease(block)
	if n := frames * Channels; n > len(c.pcm) {
		c.pcm = make([]int16, n)
	}
	pcm := c.pcm[:frames*Channels]
	channels := int(asbd.channelsPerFrame)
	if asbd.formatFlags&audioFlagNonInterleave != 0 {
		if int(c.abl.count) < 1 || int(c.abl.buffers[0].size) < frames*4 {
			c.rejected.Add(1)
			return
		}
		left := unsafe.Slice((*float32)(c.abl.buffers[0].data), frames)
		right := left
		if c.abl.count > 1 && int(c.abl.buffers[1].size) >= frames*4 {
			right = unsafe.Slice((*float32)(c.abl.buffers[1].data), frames)
		}
		interleaveFloat(pcm, left, right)
	} else {
		if c.abl.count < 1 || int(c.abl.buffers[0].size) < frames*channels*4 {
			c.rejected.Add(1)
			return
		}
		stereoFromInterleavedFloat(pcm, unsafe.Slice((*float32)(c.abl.buffers[0].data), frames*channels), channels)
	}
	pts := apple.Now()
	if cmSampleBufferGetSampleTimingInfo(sbuf, 0, unsafe.Pointer(&c.timing)) == 0 {
		if t := c.timing.pts; t.Timescale > 0 && t.Flags&1 != 0 {
			ts := int64(t.Timescale)
			pts = t.Value/ts*1e9 + t.Value%ts*1e9/ts
		}
	}
	c.onPCM(pcm, pts)
}

func (c *SCKCapturer) Start(ctx context.Context, onPCM func(pcm []int16, pts int64)) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stream != 0 {
		return errors.New("audio: capture already started")
	}
	audioClassOnce.Do(registerAudioClass)
	if audioClassErr != nil {
		return audioClassErr
	}
	content, err := shareableContent()
	if err != nil {
		return err
	}
	defer content.Send(sel("release"))
	displays := content.Send(sel("displays"))
	main := cgMainDisplayID()
	var display objc.ID
	for i := 0; i < objc.Send[int](displays, sel("count")); i++ {
		if d := displays.Send(sel("objectAtIndex:"), i); objc.Send[uint32](d, sel("displayID")) == main {
			display = d
		}
	}
	if display == 0 {
		return errors.New("audio: main display not in shareable content")
	}
	empty := objc.ID(objc.GetClass("NSArray")).Send(sel("array"))
	filter := objc.ID(objc.GetClass("SCContentFilter")).Send(sel("alloc")).Send(sel("initWithDisplay:excludingWindows:"), display, empty)
	cfg := objc.ID(objc.GetClass("SCStreamConfiguration")).Send(sel("new"))
	cfg.Send(sel("setCapturesAudio:"), true)
	cfg.Send(sel("setSampleRate:"), SampleRate)
	cfg.Send(sel("setChannelCount:"), Channels)
	cfg.Send(sel("setExcludesCurrentProcessAudio:"), true)
	// SCK always runs a video stream; keep it tiny and slow.
	cfg.Send(sel("setWidth:"), uint(16))
	cfg.Send(sel("setHeight:"), uint(16))
	cfg.Send(sel("setMinimumFrameInterval:"), apple.Time(1, 1))
	stream := objc.ID(objc.GetClass("SCStream")).Send(sel("alloc")).Send(sel("initWithFilter:configuration:delegate:"), filter, cfg, objc.ID(0))
	filter.Send(sel("release"))
	cfg.Send(sel("release"))

	c.onPCM = onPCM
	c.output = objc.ID(objc.GetClass("PlanckAudioOutput")).Send(sel("new"))
	capturers.Store(c.output, c)
	queue := dispatchQueueCreate("gg.planck.audio", 0)
	for _, kind := range []int{scStreamOutputScreen, scStreamOutputAudio} {
		var nsErr objc.ID
		if !objc.Send[bool](stream, sel("addStreamOutput:type:sampleHandlerQueue:error:"), c.output, kind, queue, unsafe.Pointer(&nsErr)) {
			capturers.Delete(c.output)
			stream.Send(sel("release"))
			return fmt.Errorf("audio: sck addStreamOutput: %w", nsError(nsErr))
		}
	}
	started := make(chan error, 1)
	block := objc.NewBlock(func(_ objc.Block, err objc.ID) { started <- nsError(err) })
	defer block.Release()
	stream.Send(sel("startCaptureWithCompletionHandler:"), block)
	select {
	case err := <-started:
		if err != nil {
			capturers.Delete(c.output)
			stream.Send(sel("release"))
			return fmt.Errorf("audio: sck start: %w", err)
		}
	case <-time.After(5 * time.Second):
		capturers.Delete(c.output)
		return errors.New("audio: sck start timed out")
	}
	c.stream = stream
	ctx, c.cancel = context.WithCancel(ctx)
	go func() {
		<-ctx.Done()
		c.stop()
	}()
	return nil
}

func shareableContent() (objc.ID, error) {
	type result struct {
		content objc.ID
		err     error
	}
	ch := make(chan result, 1)
	block := objc.NewBlock(func(_ objc.Block, content, err objc.ID) {
		if content != 0 {
			content.Send(sel("retain"))
		}
		ch <- result{content, nsError(err)}
	})
	defer block.Release()
	objc.ID(objc.GetClass("SCShareableContent")).Send(sel("getShareableContentWithCompletionHandler:"), block)
	select {
	case r := <-ch:
		if r.err != nil {
			return 0, fmt.Errorf("audio: sck shareable content: %w", r.err)
		}
		return r.content, nil
	case <-time.After(5 * time.Second):
		return 0, errors.New("audio: sck shareable content timed out")
	}
}

func (c *SCKCapturer) Close() error {
	c.mu.Lock()
	cancel := c.cancel
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	c.stop()
	return nil
}

func (c *SCKCapturer) stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stream == 0 || c.stopped {
		return
	}
	c.stopped = true
	done := make(chan struct{}, 1)
	block := objc.NewBlock(func(_ objc.Block, _ objc.ID) { done <- struct{}{} })
	defer block.Release()
	c.stream.Send(sel("stopCaptureWithCompletionHandler:"), block)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
	capturers.Delete(c.output)
	c.stream.Send(sel("release"))
}

var _ Capturer = (*SCKCapturer)(nil)
