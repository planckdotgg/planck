//go:build darwin

// Package sck captures a macOS display with ScreenCaptureKit through purego,
// with no cgo. ScreenCaptureKit calls back on a GCD serial queue thread; the
// callback runs Go code directly on that foreign thread.
package sck

import (
	"errors"
	"fmt"
	"sync"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"

	"github.com/watzon/planck/internal/apple"
	"github.com/watzon/planck/pkg/capture"
)

var (
	sckLib = func() uintptr {
		h, err := purego.Dlopen("/System/Library/Frameworks/ScreenCaptureKit.framework/ScreenCaptureKit", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			panic(err)
		}
		return h
	}()

	dispatchQueueCreate                     func(label string, attr uintptr) uintptr
	cmSampleBufferGetImageBuffer            func(sbuf uintptr) uintptr
	cmSampleBufferGetSampleAttachmentsArray func(sbuf uintptr, create bool) uintptr
	cgPreflightScreenCaptureAccess          func() bool
	cgMainDisplayID                         func() uint32

	keyStatus, keyDisplayTime uintptr
)

func init() {
	apple.Register(&dispatchQueueCreate, apple.System, "dispatch_queue_create")
	apple.Register(&cmSampleBufferGetImageBuffer, apple.CoreMedia, "CMSampleBufferGetImageBuffer")
	apple.Register(&cmSampleBufferGetSampleAttachmentsArray, apple.CoreMedia, "CMSampleBufferGetSampleAttachmentsArray")
	apple.Register(&cgPreflightScreenCaptureAccess, apple.CoreGraphics, "CGPreflightScreenCaptureAccess")
	apple.Register(&cgMainDisplayID, apple.CoreGraphics, "CGMainDisplayID")
	keyStatus = apple.Const(sckLib, "SCStreamFrameInfoStatus")
	keyDisplayTime = apple.Const(sckLib, "SCStreamFrameInfoDisplayTime")
}

// Allowed reports whether this process (or its responsible parent) holds the
// Screen Recording permission.
func Allowed() bool { return cgPreflightScreenCaptureAccess() }

func sel(s string) objc.SEL { return objc.RegisterName(s) }

func nsError(err objc.ID) error {
	if err == 0 {
		return nil
	}
	desc := err.Send(sel("localizedDescription"))
	return errors.New(goString(desc))
}

func goString(ns objc.ID) string {
	if ns == 0 {
		return ""
	}
	p := objc.Send[*byte](ns, sel("UTF8String"))
	if p == nil {
		return ""
	}
	var b []byte
	for i := uintptr(0); ; i++ {
		c := *(*byte)(unsafe.Add(unsafe.Pointer(p), i))
		if c == 0 {
			break
		}
		b = append(b, c)
	}
	return string(b)
}

// Sample is one delivered frame. Pixels is a retained CVPixelBufferRef that
// the receiver must release with apple.CFRelease.
type Sample struct {
	Pixels      uintptr
	DisplayTime int64 // mach-clock ns when the frame was shown
	Delivered   int64 // mach-clock ns when the callback started
}

type Stats struct {
	Complete, Idle, Other, Dropped int
}

type Source struct {
	stream objc.ID
	output objc.ID
	width  int
	height int
	format uint32
	frames chan Sample

	mu    sync.Mutex
	stats Stats
	cur   Sample
}

var (
	classOnce sync.Once
	classErr  error
	sources   sync.Map // output ID -> *Source
)

func registerClass() {
	_, classErr = objc.RegisterClass("PlanckStreamOutput", objc.GetClass("NSObject"),
		[]*objc.Protocol{objc.GetProtocol("SCStreamOutput")}, nil,
		[]objc.MethodDef{{
			Cmd: sel("stream:didOutputSampleBuffer:ofType:"),
			Fn:  didOutput,
		}})
}

func didOutput(self objc.ID, _ objc.SEL, _ objc.ID, sbuf uintptr, kind int) {
	delivered := apple.Now()
	v, ok := sources.Load(self)
	if !ok || kind != 0 {
		return
	}
	s := v.(*Source)
	status, display := int64(-1), int64(0)
	if arr := cmSampleBufferGetSampleAttachmentsArray(sbuf, false); arr != 0 && apple.CFArrayGetCount(arr) > 0 {
		d := apple.CFArrayGetValueAtIndex(arr, 0)
		status, _ = apple.NumberInt64(apple.CFDictionaryGetValue(d, keyStatus))
		if t, ok := apple.NumberInt64(apple.CFDictionaryGetValue(d, keyDisplayTime)); ok {
			display = apple.Nanos(uint64(t))
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch status {
	case 0:
		s.stats.Complete++
	case 1:
		s.stats.Idle++
		return
	default:
		s.stats.Other++
		return
	}
	pb := cmSampleBufferGetImageBuffer(sbuf)
	if pb == 0 {
		return
	}
	apple.CFRetain(pb)
	select {
	case s.frames <- Sample{Pixels: pb, DisplayTime: display, Delivered: delivered}:
	default:
		s.stats.Dropped++
		apple.CFRelease(pb)
	}
}

// Open starts capturing the main display scaled to width x height at up to
// fps, in pixel format (vt.PixelBGRA or vt.Pixel420v).
func Open(width, height, fps int, format uint32) (*Source, error) {
	classOnce.Do(registerClass)
	if classErr != nil {
		return nil, classErr
	}
	content, err := shareableContent()
	if err != nil {
		return nil, err
	}
	defer content.Send(sel("release"))
	displays := content.Send(sel("displays"))
	main := cgMainDisplayID()
	var display objc.ID
	for i := 0; i < objc.Send[int](displays, sel("count")); i++ {
		d := displays.Send(sel("objectAtIndex:"), i)
		if objc.Send[uint32](d, sel("displayID")) == main {
			display = d
		}
	}
	if display == 0 {
		return nil, errors.New("sck: main display not in shareable content")
	}
	empty := objc.ID(objc.GetClass("NSArray")).Send(sel("array"))
	filter := objc.ID(objc.GetClass("SCContentFilter")).Send(sel("alloc")).Send(sel("initWithDisplay:excludingWindows:"), display, empty)
	cfg := objc.ID(objc.GetClass("SCStreamConfiguration")).Send(sel("new"))
	cfg.Send(sel("setWidth:"), uint(width))
	cfg.Send(sel("setHeight:"), uint(height))
	cfg.Send(sel("setMinimumFrameInterval:"), apple.Time(1, int32(fps)))
	cfg.Send(sel("setPixelFormat:"), format)
	cfg.Send(sel("setQueueDepth:"), 4)
	cfg.Send(sel("setShowsCursor:"), false)
	stream := objc.ID(objc.GetClass("SCStream")).Send(sel("alloc")).Send(sel("initWithFilter:configuration:delegate:"), filter, cfg, objc.ID(0))
	filter.Send(sel("release"))
	cfg.Send(sel("release"))

	s := &Source{stream: stream, width: width, height: height, format: format, frames: make(chan Sample, 2)}
	s.output = objc.ID(objc.GetClass("PlanckStreamOutput")).Send(sel("new"))
	sources.Store(s.output, s)
	queue := dispatchQueueCreate("gg.planck.sck", 0)
	var nsErr objc.ID
	if !objc.Send[bool](stream, sel("addStreamOutput:type:sampleHandlerQueue:error:"), s.output, 0, queue, unsafe.Pointer(&nsErr)) {
		return nil, fmt.Errorf("sck: addStreamOutput: %w", nsError(nsErr))
	}
	started := make(chan error, 1)
	block := objc.NewBlock(func(_ objc.Block, err objc.ID) { started <- nsError(err) })
	defer block.Release()
	stream.Send(sel("startCaptureWithCompletionHandler:"), block)
	select {
	case err := <-started:
		if err != nil {
			return nil, fmt.Errorf("sck: start: %w", err)
		}
	case <-time.After(5 * time.Second):
		return nil, errors.New("sck: start timed out")
	}
	return s, nil
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
			return 0, fmt.Errorf("sck: shareable content: %w", r.err)
		}
		return r.content, nil
	case <-time.After(5 * time.Second):
		return 0, errors.New("sck: shareable content timed out")
	}
}

// NextSample waits for the next complete frame.
func (s *Source) NextSample(timeout time.Duration) (Sample, error) {
	select {
	case f := <-s.frames:
		return f, nil
	case <-time.After(timeout):
		return Sample{}, capture.ErrTimeout
	}
}

// Next adapts NextSample to capture.Source. Presented and Acquired are
// mach-clock nanoseconds.
func (s *Source) Next(timeout time.Duration) (capture.Frame, error) {
	smp, err := s.NextSample(timeout)
	if err != nil {
		return capture.Frame{}, err
	}
	s.Release()
	s.cur = smp
	f := capture.Format(capture.FormatBGRA8)
	if s.format == 0x34323076 {
		f = capture.FormatNV12
	}
	return capture.Frame{Surface: smp.Pixels, Format: f, Width: s.width, Height: s.height,
		Presented: time.Duration(smp.DisplayTime), Acquired: time.Duration(smp.Delivered)}, nil
}

func (s *Source) Release() error {
	if s.cur.Pixels != 0 {
		apple.CFRelease(s.cur.Pixels)
		s.cur = Sample{}
	}
	return nil
}

func (s *Source) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stats
}

func (s *Source) Close() error {
	done := make(chan struct{}, 1)
	block := objc.NewBlock(func(_ objc.Block, _ objc.ID) { done <- struct{}{} })
	defer block.Release()
	s.stream.Send(sel("stopCaptureWithCompletionHandler:"), block)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
	s.Release()
	sources.Delete(s.output)
	return nil
}

var _ capture.Source = (*Source)(nil)
