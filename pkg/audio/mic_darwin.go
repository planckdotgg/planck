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

	"github.com/planckdotgg/planck/internal/apple"
)

const (
	queueBuffers   = 3
	queueFrameTime = 10 * time.Millisecond
)

// Audio queue callbacks carry an integer handle that maps to their owner.
var (
	queueCallbacksOnce sync.Once
	inputCallback      uintptr
	outputCallback     uintptr
	queues             sync.Map // uintptr -> *aqMic or *aqSink
	queueNext          atomic.Uintptr
)

func initQueueCallbacks() {
	inputCallback = purego.NewCallback(func(user, aq uintptr, buf *audioQueueBuffer, start *audioTimeStamp, packets uint32, descs unsafe.Pointer) {
		if v, ok := queues.Load(user); ok {
			v.(*aqMic).deliver(aq, buf, start)
		}
	})
	outputCallback = purego.NewCallback(func(user, aq uintptr, buf *audioQueueBuffer) {
		if v, ok := queues.Load(user); ok {
			v.(*aqSink).fill(aq, buf)
		}
	})
}

// newQueue makes an input or output audio queue of 48 kHz int16 with its
// buffers, routed to owner, on the device with uid, or the default device
// when uid is empty.
func newQueue(owner any, input bool, channels int, uid string) (aq, handle uintptr, bufs [queueBuffers]*audioQueueBuffer, err error) {
	queueCallbacksOnce.Do(initQueueCallbacks)
	handle = queueNext.Add(1)
	queues.Store(handle, owner)
	format := int16Format(channels)
	if input {
		err = osStatus("AudioQueueNewInput", audioQueueNewInput(&format, inputCallback, handle, 0, 0, 0, &aq))
	} else {
		err = osStatus("AudioQueueNewOutput", audioQueueNewOutput(&format, outputCallback, handle, 0, 0, 0, &aq))
	}
	if err != nil {
		queues.Delete(handle)
		return 0, 0, bufs, err
	}
	if uid != "" {
		s := apple.String(uid)
		err = osStatus("set queue device", audioQueueSetProperty(aq, aqCurrentDevice, unsafe.Pointer(&s), uint32(unsafe.Sizeof(s))))
		apple.CFRelease(s)
		if err != nil {
			audioQueueDispose(aq, true)
			queues.Delete(handle)
			return 0, 0, bufs, err
		}
	}
	size := uint32(FrameSamples(queueFrameTime) * channels * 2)
	for i := range bufs {
		if err = osStatus("AudioQueueAllocateBuffer", audioQueueAllocateBuffer(aq, size, &bufs[i])); err != nil {
			audioQueueDispose(aq, true)
			queues.Delete(handle)
			return 0, 0, bufs, err
		}
	}
	return aq, handle, bufs, nil
}

// aqMic records an input device with an audio queue. macOS asks the user
// for the Microphone permission the first time.
type aqMic struct {
	uid    string
	mu     sync.Mutex
	aq     uintptr
	handle uintptr
	onPCM  func([]int16, int64)
	cancel context.CancelFunc
	pcm    []int16
}

// NewMicCapturer records the default input device.
func NewMicCapturer() (MicCapturer, error) { return &aqMic{}, nil }

// NewMicCapturerOn records the input device with this Device ID, or the
// default one when id is empty or the device is gone.
func NewMicCapturerOn(id string) (MicCapturer, error) {
	return &aqMic{uid: presentDevice(InputDevices, id)}, nil
}

func (m *aqMic) Start(ctx context.Context, onPCM func(pcm []int16, pts int64)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.aq != 0 {
		return errors.New("audio: capture already started")
	}
	m.onPCM = onPCM
	aq, handle, bufs, err := newQueue(m, true, MicChannels, m.uid)
	if err != nil {
		return err
	}
	for _, b := range bufs {
		audioQueueEnqueueBuffer(aq, b, 0, 0)
	}
	if err := osStatus("AudioQueueStart", audioQueueStart(aq, 0)); err != nil {
		audioQueueDispose(aq, true)
		queues.Delete(handle)
		return err
	}
	m.aq, m.handle = aq, handle
	ctx, m.cancel = context.WithCancel(ctx)
	go func() {
		<-ctx.Done()
		m.stop()
	}()
	return nil
}

func (m *aqMic) deliver(aq uintptr, b *audioQueueBuffer, start *audioTimeStamp) {
	n := int(b.size) / 2
	if n > 0 && b.data != nil {
		if n > len(m.pcm) {
			m.pcm = make([]int16, n)
		}
		pcm := m.pcm[:n]
		copy(pcm, unsafe.Slice((*int16)(b.data), n))
		pts := apple.Now()
		if start != nil && start.hostTime != 0 {
			pts = apple.Nanos(start.hostTime)
		}
		m.onPCM(pcm, pts)
	}
	audioQueueEnqueueBuffer(aq, b, 0, 0)
}

func (m *aqMic) stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.aq == 0 {
		return
	}
	audioQueueStop(m.aq, true)
	audioQueueDispose(m.aq, true)
	queues.Delete(m.handle)
	m.aq = 0
}

func (m *aqMic) Close() error {
	m.mu.Lock()
	cancel := m.cancel
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	m.stop()
	return nil
}

// aqSink plays PCM into one output device with an audio queue.
type aqSink struct {
	dev    VirtualMic
	ring   *pcmRing
	aq     uintptr
	handle uintptr
	mu     sync.Mutex
	closed bool
}

// OpenMicSink plays mic PCM into dev, found with FindVirtualMic.
func OpenMicSink(dev VirtualMic) (MicSink, error) {
	s, err := openQueueSink(dev, MicChannels)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// openOutput plays stereo PCM on output device d.
func openOutput(d Device) (Player, error) {
	s, err := openQueueSink(VirtualMic{ID: d.ID, Name: d.Name}, Channels)
	if err != nil {
		return nil, err
	}
	return s, nil
}

func openQueueSink(dev VirtualMic, channels int) (*aqSink, error) {
	s := &aqSink{dev: dev, ring: newPCMRing(FrameSamples(60*time.Millisecond) * channels)}
	aq, handle, bufs, err := newQueue(s, false, channels, dev.ID)
	if err != nil {
		return nil, fmt.Errorf("audio: open %s: %w", dev.Name, err)
	}
	s.aq, s.handle = aq, handle
	for _, b := range bufs {
		s.fill(aq, b)
	}
	if err := osStatus("AudioQueueStart", audioQueueStart(aq, 0)); err != nil {
		audioQueueDispose(aq, true)
		queues.Delete(handle)
		return nil, fmt.Errorf("audio: open %s: %w", dev.Name, err)
	}
	return s, nil
}

// fill hands the queue the next buffer: queued PCM, then silence, since
// an output queue stops when it runs out of buffers.
func (s *aqSink) fill(aq uintptr, b *audioQueueBuffer) {
	dst := unsafe.Slice((*byte)(b.data), b.capacity)
	n := s.ring.read(dst)
	clear(dst[n:])
	b.size = b.capacity
	audioQueueEnqueueBuffer(aq, b, 0, 0)
}

func (s *aqSink) Device() string { return s.dev.Name }

func (s *aqSink) Write(pcm []int16) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	s.ring.write(pcm)
	return nil
}

func (s *aqSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	audioQueueStop(s.aq, true)
	audioQueueDispose(s.aq, true)
	queues.Delete(s.handle)
	return nil
}

var (
	_ MicCapturer = (*aqMic)(nil)
	_ MicSink     = (*aqSink)(nil)
)
