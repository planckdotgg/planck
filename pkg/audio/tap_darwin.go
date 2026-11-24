//go:build darwin

package audio

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"

	"github.com/planckdotgg/planck/internal/apple"
)

var (
	hasProcessTap bool

	audioHardwareCreateProcessTap  func(desc objc.ID, out *uint32) int32
	audioHardwareDestroyProcessTap func(tap uint32) int32
	audioHardwareCreateAggregate   func(desc objc.ID, out *uint32) int32
	audioHardwareDestroyAggregate  func(dev uint32) int32
	audioDeviceCreateIOProcID      func(dev uint32, proc uintptr, user uintptr, out *uintptr) int32
	audioDeviceDestroyIOProcID     func(dev uint32, proc uintptr) int32
	audioDeviceStart               func(dev uint32, proc uintptr) int32
	audioDeviceStop                func(dev uint32, proc uintptr) int32
)

func init() {
	// The process tap API arrived in macOS 14.2.
	hasProcessTap = apple.HasSym(coreAudioLib, "AudioHardwareCreateProcessTap") && objc.GetClass("CATapDescription") != 0
	if !hasProcessTap {
		return
	}
	apple.Register(&audioHardwareCreateProcessTap, coreAudioLib, "AudioHardwareCreateProcessTap")
	apple.Register(&audioHardwareDestroyProcessTap, coreAudioLib, "AudioHardwareDestroyProcessTap")
	apple.Register(&audioHardwareCreateAggregate, coreAudioLib, "AudioHardwareCreateAggregateDevice")
	apple.Register(&audioHardwareDestroyAggregate, coreAudioLib, "AudioHardwareDestroyAggregateDevice")
	apple.Register(&audioDeviceCreateIOProcID, coreAudioLib, "AudioDeviceCreateIOProcID")
	apple.Register(&audioDeviceDestroyIOProcID, coreAudioLib, "AudioDeviceDestroyIOProcID")
	apple.Register(&audioDeviceStart, coreAudioLib, "AudioDeviceStart")
	apple.Register(&audioDeviceStop, coreAudioLib, "AudioDeviceStop")
}

// SystemCapturer records the desktop mix without Planck's own process: a
// Core Audio process tap on macOS 14.2 and later, else ScreenCaptureKit.
type SystemCapturer struct {
	opts CaptureOptions
	mu   sync.Mutex
	cur  Capturer
	mode string
	// TapErr is why the process tap was not used, when it was not.
	tapErr error
}

func NewCapturer(opts CaptureOptions) (*SystemCapturer, error) {
	return &SystemCapturer{opts: opts}, nil
}

func (c *SystemCapturer) Start(ctx context.Context, onPCM func(pcm []int16, pts int64)) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cur != nil {
		return errors.New("audio: capture already started")
	}
	tap := &tapCapturer{opts: c.opts}
	if err := tap.Start(ctx, onPCM); err == nil {
		c.cur, c.mode = tap, ModeProcessTap
		return nil
	} else {
		c.tapErr = err
	}
	sck := newSCKCapturer()
	if err := sck.Start(ctx, onPCM); err != nil {
		return fmt.Errorf("%w (process tap: %v)", err, c.tapErr)
	}
	c.cur, c.mode = sck, ModeScreenCaptureKit
	return nil
}

// Mode says which capture path Start opened.
func (c *SystemCapturer) Mode() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.mode
}

// TapError is why Start fell back from the process tap, or nil.
func (c *SystemCapturer) TapError() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tapErr
}

func (c *SystemCapturer) Close() error {
	c.mu.Lock()
	cur := c.cur
	c.mu.Unlock()
	if cur == nil {
		return nil
	}
	return cur.Close()
}

// ioProcs routes HAL IOProc callbacks, which carry an integer handle, to
// their capturer.
var (
	ioProcOnce sync.Once
	ioProcFn   uintptr
	ioProcs    sync.Map // uintptr -> *tapCapturer
	ioProcNext atomic.Uintptr
)

func tapIOProc(dev uint32, now unsafe.Pointer, input *audioBufferList, inputTime *audioTimeStamp, output, outputTime unsafe.Pointer, user uintptr) int32 {
	if v, ok := ioProcs.Load(user); ok {
		v.(*tapCapturer).deliver(input, inputTime)
	}
	return 0
}

// tapCapturer is a global stereo process tap, excluding Planck and
// opts.ExcludePIDs, read through a private aggregate device.
type tapCapturer struct {
	opts   CaptureOptions
	onPCM  func([]int16, int64)
	tap    uint32
	agg    uint32
	procID uintptr
	handle uintptr
	format streamBasicDescription
	once   sync.Once
	cancel context.CancelFunc

	// Used only on the IOProc thread.
	pcm []int16
}

func (c *tapCapturer) Start(ctx context.Context, onPCM func(pcm []int16, pts int64)) (err error) {
	if !hasProcessTap {
		return errors.New("audio: process taps need macOS 14.2")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	pool := objc.ID(objc.GetClass("NSAutoreleasePool")).Send(sel("new"))
	defer pool.Send(sel("drain"))
	pids := append([]int{os.Getpid()}, c.opts.ExcludePIDs...)
	exclude := objc.ID(objc.GetClass("NSMutableArray")).Send(sel("array"))
	for _, pid := range pids {
		if obj := processObject(int32(pid)); obj != 0 {
			exclude.Send(sel("addObject:"), objc.ID(objc.GetClass("NSNumber")).Send(sel("numberWithUnsignedInt:"), obj))
		}
	}
	desc := objc.ID(objc.GetClass("CATapDescription")).Send(sel("alloc")).Send(sel("initStereoGlobalTapButExcludeProcesses:"), exclude)
	if desc == 0 {
		return errors.New("audio: CATapDescription init failed")
	}
	defer desc.Send(sel("release"))
	desc.Send(sel("setPrivate:"), true)
	if err := osStatus("AudioHardwareCreateProcessTap", audioHardwareCreateProcessTap(desc, &c.tap)); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			c.release()
		}
	}()
	if err := getProp(c.tap, selTapFormat, scopeGlobal, unsafe.Pointer(&c.format), uint32(unsafe.Sizeof(c.format))); err != nil {
		return err
	}
	f := c.format
	if f.formatID != audioFormatLinearPCM || f.formatFlags&audioFlagIsFloat == 0 || f.bitsPerChannel != 32 || f.channelsPerFrame == 0 {
		return fmt.Errorf("audio: process tap format %+v is not float32 PCM", f)
	}
	if f.sampleRate != SampleRate {
		return fmt.Errorf("audio: process tap runs at %.0f Hz, not %d", f.sampleRate, SampleRate)
	}
	tapUID := goString(desc.Send(sel("UUID")).Send(sel("UUIDString")))
	ns := func(s string) objc.ID {
		return objc.ID(objc.GetClass("NSString")).Send(sel("stringWithUTF8String:"), s)
	}
	dict := func(kv ...objc.ID) objc.ID {
		d := objc.ID(objc.GetClass("NSMutableDictionary")).Send(sel("dictionary"))
		for i := 0; i+1 < len(kv); i += 2 {
			d.Send(sel("setObject:forKey:"), kv[i+1], kv[i])
		}
		return d
	}
	array := func(v objc.ID) objc.ID { return objc.ID(objc.GetClass("NSArray")).Send(sel("arrayWithObject:"), v) }
	yes := objc.ID(objc.GetClass("NSNumber")).Send(sel("numberWithBool:"), true)
	no := objc.ID(objc.GetClass("NSNumber")).Send(sel("numberWithBool:"), false)
	aggDesc := dict(
		ns("name"), ns("Planck Desktop Audio"),
		ns("uid"), ns("gg.planck.tap."+tapUID),
		ns("private"), yes,
		ns("stacked"), no,
		ns("tapautostart"), yes,
		ns("taps"), array(dict(ns("uid"), ns(tapUID), ns("drift"), yes)),
	)
	var output uint32
	if getProp(systemObject, selDefaultOutput, scopeGlobal, unsafe.Pointer(&output), 4) == nil && output != 0 {
		if uid := propString(output, selDeviceUID, scopeGlobal); uid != "" {
			aggDesc.Send(sel("setObject:forKey:"), ns(uid), ns("master"))
			aggDesc.Send(sel("setObject:forKey:"), array(dict(ns("uid"), ns(uid))), ns("subdevices"))
		}
	}
	if err := osStatus("AudioHardwareCreateAggregateDevice", audioHardwareCreateAggregate(aggDesc, &c.agg)); err != nil {
		return err
	}
	ioProcOnce.Do(func() { ioProcFn = purego.NewCallback(tapIOProc) })
	c.onPCM = onPCM
	c.handle = ioProcNext.Add(1)
	ioProcs.Store(c.handle, c)
	if err := osStatus("AudioDeviceCreateIOProcID", audioDeviceCreateIOProcID(c.agg, ioProcFn, c.handle, &c.procID)); err != nil {
		return err
	}
	if err := osStatus("AudioDeviceStart", audioDeviceStart(c.agg, c.procID)); err != nil {
		return err
	}
	ctx, c.cancel = context.WithCancel(ctx)
	go func() {
		<-ctx.Done()
		c.release()
	}()
	return nil
}

func (c *tapCapturer) deliver(abl *audioBufferList, inputTime *audioTimeStamp) {
	if abl == nil {
		return
	}
	if abl.count == 0 {
		return
	}
	channels := int(c.format.channelsPerFrame)
	b0 := abl.buffers[0]
	var frames int
	nonInterleaved := c.format.formatFlags&audioFlagNonInterleave != 0
	if nonInterleaved {
		frames = int(b0.size) / 4
	} else {
		frames = int(b0.size) / (4 * channels)
	}
	if frames == 0 || b0.data == nil {
		return
	}
	if n := frames * Channels; n > len(c.pcm) {
		c.pcm = make([]int16, n)
	}
	pcm := c.pcm[:frames*Channels]
	if nonInterleaved {
		left := unsafe.Slice((*float32)(b0.data), frames)
		right := left
		if abl.count > 1 {
			b1 := abl.buffers[1]
			if int(b1.size) >= frames*4 && b1.data != nil {
				right = unsafe.Slice((*float32)(b1.data), frames)
			}
		}
		interleaveFloat(pcm, left, right)
	} else {
		stereoFromInterleavedFloat(pcm, unsafe.Slice((*float32)(b0.data), frames*channels), channels)
	}
	pts := apple.Now()
	if inputTime != nil && inputTime.hostTime != 0 {
		pts = apple.Nanos(inputTime.hostTime)
	}
	c.onPCM(pcm, pts)
}

func (c *tapCapturer) release() {
	c.once.Do(func() {
		if c.procID != 0 {
			audioDeviceStop(c.agg, c.procID)
			audioDeviceDestroyIOProcID(c.agg, c.procID)
		}
		if c.handle != 0 {
			ioProcs.Delete(c.handle)
		}
		if c.agg != 0 {
			audioHardwareDestroyAggregate(c.agg)
		}
		if c.tap != 0 {
			audioHardwareDestroyProcessTap(c.tap)
		}
	})
}

func (c *tapCapturer) Close() error {
	if c.cancel != nil {
		c.cancel()
	}
	c.release()
	return nil
}

var (
	_ Capturer = (*SystemCapturer)(nil)
	_ Capturer = (*tapCapturer)(nil)
)
