//go:build windows

package audio

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

var (
	ole32            = syscall.NewLazyDLL("ole32.dll")
	procCoInit       = ole32.NewProc("CoInitializeEx")
	procCoUninit     = ole32.NewProc("CoUninitialize")
	procCoCreate     = ole32.NewProc("CoCreateInstance")
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	procCreateEvent  = kernel32.NewProc("CreateEventW")
	procQPC          = kernel32.NewProc("QueryPerformanceCounter")
	procQPF          = kernel32.NewProc("QueryPerformanceFrequency")
	avrt             = syscall.NewLazyDLL("avrt.dll")
	procAvSetMmTask  = avrt.NewProc("AvSetMmThreadCharacteristicsW")
	procAvRevertTask = avrt.NewProc("AvRevertMmThreadCharacteristics")
)

type guid struct {
	d1     uint32
	d2, d3 uint16
	d4     [8]byte
}

var (
	clsidMMDeviceEnumerator = guid{0xBCDE0395, 0xE52F, 0x467C, [8]byte{0x8E, 0x3D, 0xC4, 0x57, 0x92, 0x91, 0x69, 0x2E}}
	iidIMMDeviceEnumerator  = guid{0xA95664D2, 0x9614, 0x4F35, [8]byte{0xA7, 0x46, 0xDE, 0x8D, 0xB6, 0x36, 0x17, 0xE6}}
	iidIAudioClient         = guid{0x1CB9AD4C, 0xDBFA, 0x4C32, [8]byte{0xB1, 0x78, 0xC2, 0xF5, 0x68, 0xA7, 0x03, 0xB2}}
	iidIAudioCaptureClient  = guid{0xC8ADBD64, 0xE71E, 0x48A0, [8]byte{0xA4, 0xDE, 0x18, 0x5C, 0x39, 0x5C, 0xD3, 0x17}}
)

const (
	clsctxAll             = 0x17
	eRender               = 0
	eConsole              = 0
	shareModeShared       = 0
	streamFlagsLoopback   = 0x00020000
	streamFlagsEvent      = 0x00040000
	streamFlagsAutoConv   = 0x80000000
	streamFlagsSRCDefault = 0x08000000
	bufferFlagsSilent     = 0x2
	waveFormatPCM         = 1

	// vtable slots
	enumGetDefaultAudioEndpoint = 4
	deviceActivate              = 3
	clientInitialize            = 3
	clientStart                 = 10
	clientStop                  = 11
	clientSetEventHandle        = 13
	clientGetService            = 14
	captureGetBuffer            = 3
	captureReleaseBuffer        = 4
	captureGetNextPacketSize    = 5
)

// comObject is a COM interface pointer; its first word is the vtable.
type comObject struct{ vtbl *[32]uintptr }

// The call wrappers take Go pointers as uintptr; uintptrescapes keeps their
// targets on the heap so a stack move cannot invalidate them mid-call.

//go:uintptrescapes
func (o *comObject) call(slot int, args ...uintptr) uintptr {
	r, _, _ := syscall.SyscallN(o.vtbl[slot], append([]uintptr{uintptr(unsafe.Pointer(o))}, args...)...)
	return r
}

//go:uintptrescapes
func (o *comObject) call1(slot int, a uintptr) uintptr {
	r, _, _ := syscall.SyscallN(o.vtbl[slot], uintptr(unsafe.Pointer(o)), a)
	return r
}

//go:uintptrescapes
func (o *comObject) call5(slot int, a, b, c, d, e uintptr) uintptr {
	r, _, _ := syscall.SyscallN(o.vtbl[slot], uintptr(unsafe.Pointer(o)), a, b, c, d, e)
	return r
}

func (o *comObject) release() {
	if o != nil {
		syscall.SyscallN(o.vtbl[2], uintptr(unsafe.Pointer(o)))
	}
}

func hresult(op string, r uintptr) error {
	if int32(r) < 0 {
		return fmt.Errorf("audio: wasapi %s: HRESULT 0x%08x", op, uint32(r))
	}
	return nil
}

type waveFormatEx struct {
	formatTag      uint16
	channels       uint16
	samplesPerSec  uint32
	avgBytesPerSec uint32
	blockAlign     uint16
	bitsPerSample  uint16
	size           uint16
}

// captureState holds the GetBuffer out-parameters for the whole loop, so
// reading a packet allocates nothing.
type captureState struct {
	data   unsafe.Pointer
	frames uint32
	flags  uint32
	devPos uint64
	qpcPos uint64
	packet uint32
	pcm    []int16
}

// WASAPICapturer records the default render endpoint in shared-mode loopback.
// WASAPI converts the mix to 48 kHz stereo int16 itself (AUTOCONVERTPCM).
type WASAPICapturer struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

func NewCapturer() (*WASAPICapturer, error) { return &WASAPICapturer{}, nil }

func (c *WASAPICapturer) Start(ctx context.Context, onPCM func(pcm []int16, pts int64)) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.done != nil {
		return errors.New("audio: capture already started")
	}
	ctx, cancel := context.WithCancel(ctx)
	ready := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		captureLoop(ctx, onPCM, ready)
	}()
	if err := <-ready; err != nil {
		cancel()
		<-done
		return err
	}
	c.cancel, c.done = cancel, done
	return nil
}

func (c *WASAPICapturer) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.done != nil {
		c.cancel()
		<-c.done
	}
	return nil
}

var qpcFreq = func() int64 {
	var f int64
	procQPF.Call(uintptr(unsafe.Pointer(&f)))
	return f
}()

func qpcNow() int64 {
	var t int64
	syscall.SyscallN(procQPC.Addr(), uintptr(unsafe.Pointer(&t)))
	return t/qpcFreq*1e9 + t%qpcFreq*1e9/qpcFreq
}

func captureLoop(ctx context.Context, onPCM func([]int16, int64), ready chan<- error) {
	fail := func(err error) { ready <- err }
	const coinitMultithreaded = 0
	if r, _, _ := procCoInit.Call(0, coinitMultithreaded); int32(r) < 0 {
		fail(hresult("CoInitializeEx", r))
		return
	}
	defer procCoUninit.Call()

	var enum *comObject
	r, _, _ := procCoCreate.Call(uintptr(unsafe.Pointer(&clsidMMDeviceEnumerator)), 0, clsctxAll,
		uintptr(unsafe.Pointer(&iidIMMDeviceEnumerator)), uintptr(unsafe.Pointer(&enum)))
	if err := hresult("CoCreateInstance", r); err != nil {
		fail(err)
		return
	}
	defer enum.release()
	var device *comObject
	if err := hresult("GetDefaultAudioEndpoint", enum.call(enumGetDefaultAudioEndpoint, eRender, eConsole, uintptr(unsafe.Pointer(&device)))); err != nil {
		fail(err)
		return
	}
	defer device.release()
	var client *comObject
	if err := hresult("Activate", device.call(deviceActivate, uintptr(unsafe.Pointer(&iidIAudioClient)), clsctxAll, 0, uintptr(unsafe.Pointer(&client)))); err != nil {
		fail(err)
		return
	}
	defer client.release()

	format := waveFormatEx{formatTag: waveFormatPCM, channels: Channels, samplesPerSec: SampleRate,
		avgBytesPerSec: SampleRate * Channels * 2, blockAlign: Channels * 2, bitsPerSample: 16}
	const bufferDuration = 20 * time.Millisecond / 100 // REFERENCE_TIME is 100ns units
	if err := hresult("Initialize", client.call(clientInitialize, shareModeShared,
		streamFlagsLoopback|streamFlagsEvent|streamFlagsAutoConv|streamFlagsSRCDefault,
		uintptr(bufferDuration), 0, uintptr(unsafe.Pointer(&format)), 0)); err != nil {
		fail(err)
		return
	}
	event, _, errno := procCreateEvent.Call(0, 0, 0, 0)
	if event == 0 {
		fail(fmt.Errorf("audio: CreateEventW: %w", errno))
		return
	}
	defer syscall.CloseHandle(syscall.Handle(event))
	if err := hresult("SetEventHandle", client.call1(clientSetEventHandle, event)); err != nil {
		fail(err)
		return
	}
	var capture *comObject
	if err := hresult("GetService", client.call(clientGetService, uintptr(unsafe.Pointer(&iidIAudioCaptureClient)), uintptr(unsafe.Pointer(&capture)))); err != nil {
		fail(err)
		return
	}
	defer capture.release()

	var task uint32
	if h, _, _ := procAvSetMmTask.Call(uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr("Pro Audio"))), uintptr(unsafe.Pointer(&task))); h != 0 {
		defer procAvRevertTask.Call(h)
	}
	if err := hresult("Start", client.call(clientStart)); err != nil {
		fail(err)
		return
	}
	defer client.call(clientStop)
	ready <- nil

	st := &captureState{pcm: make([]int16, SampleRate/10*Channels)}
	silence := make([]int16, FrameSamples(DefaultFrameDuration)*Channels)
	lastData := qpcNow()
	for ctx.Err() == nil {
		// Older Windows builds never signal the event in loopback mode, so
		// the timeout doubles as a poll.
		syscall.WaitForSingleObject(syscall.Handle(event), 10)
		got := false
		for {
			if hresult("GetNextPacketSize", capture.call1(captureGetNextPacketSize, uintptr(unsafe.Pointer(&st.packet)))) != nil || st.packet == 0 {
				break
			}
			if hresult("GetBuffer", capture.call5(captureGetBuffer, uintptr(unsafe.Pointer(&st.data)), uintptr(unsafe.Pointer(&st.frames)),
				uintptr(unsafe.Pointer(&st.flags)), uintptr(unsafe.Pointer(&st.devPos)), uintptr(unsafe.Pointer(&st.qpcPos)))) != nil {
				break
			}
			n := int(st.frames) * Channels
			if n > len(st.pcm) {
				st.pcm = make([]int16, n)
			}
			pcm := st.pcm[:n]
			if st.flags&bufferFlagsSilent != 0 || st.data == nil {
				clear(pcm)
			} else {
				copy(pcm, unsafe.Slice((*int16)(st.data), n))
			}
			capture.call1(captureReleaseBuffer, uintptr(st.frames))
			got = true
			onPCM(pcm, int64(st.qpcPos)*100)
		}
		now := qpcNow()
		if got {
			lastData = now
			continue
		}
		// Loopback delivers nothing while no app is playing. Fill the gap
		// with silence so the stream keeps its cadence.
		for lastData+int64(2*DefaultFrameDuration) <= now {
			lastData += int64(DefaultFrameDuration)
			onPCM(silence, lastData)
		}
	}
}

var _ Capturer = (*WASAPICapturer)(nil)
