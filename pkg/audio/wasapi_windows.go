//go:build windows

package audio

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	ole32             = syscall.NewLazyDLL("ole32.dll")
	procCoInit        = ole32.NewProc("CoInitializeEx")
	procCoUninit      = ole32.NewProc("CoUninitialize")
	procCoCreate      = ole32.NewProc("CoCreateInstance")
	procCoTaskFree    = ole32.NewProc("CoTaskMemFree")
	procPropVarClear  = ole32.NewProc("PropVariantClear")
	kernel32          = syscall.NewLazyDLL("kernel32.dll")
	procCreateEvent   = kernel32.NewProc("CreateEventW")
	procSetEvent      = kernel32.NewProc("SetEvent")
	procQPC           = kernel32.NewProc("QueryPerformanceCounter")
	procQPF           = kernel32.NewProc("QueryPerformanceFrequency")
	avrt              = syscall.NewLazyDLL("avrt.dll")
	procAvSetMmTask   = avrt.NewProc("AvSetMmThreadCharacteristicsW")
	procAvRevertTask  = avrt.NewProc("AvRevertMmThreadCharacteristics")
	mmdevapi          = syscall.NewLazyDLL("mmdevapi.dll")
	procActivateAsync = mmdevapi.NewProc("ActivateAudioInterfaceAsync")
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
	iidIAudioRenderClient   = guid{0xF294ACFC, 0x3146, 0x4483, [8]byte{0xA7, 0xBF, 0xAD, 0xDC, 0xA7, 0xC2, 0x60, 0xE2}}
	iidIUnknown             = guid{0x00000000, 0x0000, 0x0000, [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidIAgileObject         = guid{0x94EA2B94, 0xE9CC, 0x49E0, [8]byte{0xC0, 0xFF, 0xEE, 0x64, 0xCA, 0x8F, 0x5B, 0x90}}
	iidActivateCompletion   = guid{0x41D949AB, 0x9862, 0x444A, [8]byte{0x80, 0xF6, 0xC2, 0x61, 0x33, 0x4D, 0xA5, 0xEB}}
	iidEndpointVolume       = guid{0x5CDF2C82, 0x841E, 0x4546, [8]byte{0x97, 0x22, 0x0C, 0xF7, 0x40, 0x78, 0x22, 0x9A}}
	pkeyDeviceFriendlyName  = propertyKey{guid{0xA45C254E, 0xDF1C, 0x4EFD, [8]byte{0x80, 0x20, 0x67, 0xD1, 0x46, 0xA8, 0x50, 0xE0}}, 14}
)

const (
	clsctxAll             = 0x17
	eRender               = 0
	eCapture              = 1
	eConsole              = 0
	deviceStateActive     = 1
	stgmRead              = 0
	shareModeShared       = 0
	streamFlagsLoopback   = 0x00020000
	streamFlagsEvent      = 0x00040000
	streamFlagsAutoConv   = 0x80000000
	streamFlagsSRCDefault = 0x08000000
	bufferFlagsSilent     = 0x2
	waveFormatPCM         = 1
	waveFormatFloat       = 3
	vtLPWSTR              = 31
	vtBlob                = 65
	eNoInterface          = 0x80004002

	activationTypeProcessLoopback = 1
	loopbackModeExcludeTree       = 1
	processLoopbackPath           = `VAD\Process_Loopback`

	// vtable slots
	enumEnumAudioEndpoints      = 3
	enumGetDefaultAudioEndpoint = 4
	enumGetDevice               = 5
	collGetCount                = 3
	collItem                    = 4
	deviceActivate              = 3
	deviceOpenPropertyStore     = 4
	deviceGetID                 = 5
	propsGetValue               = 5
	clientInitialize            = 3
	clientGetBufferSize         = 4
	clientGetCurrentPadding     = 6
	clientGetMixFormat          = 8
	clientStart                 = 10
	clientStop                  = 11
	clientSetEventHandle        = 13
	clientGetService            = 14
	captureGetBuffer            = 3
	captureReleaseBuffer        = 4
	captureGetNextPacketSize    = 5
	renderGetBuffer             = 3
	renderReleaseBuffer         = 4
	activateOpGetResult         = 3
	volGetMasterLevel           = 8
	volSetMute                  = 14
	volGetMute                  = 15
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
func (o *comObject) call2(slot int, a, b uintptr) uintptr {
	r, _, _ := syscall.SyscallN(o.vtbl[slot], uintptr(unsafe.Pointer(o)), a, b)
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

//go:uintptrescapes
func callProc(p *syscall.LazyProc, args ...uintptr) uintptr {
	r, _, _ := syscall.SyscallN(p.Addr(), args...)
	return r
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

func pcmFormat(channels int) *waveFormatEx {
	return &waveFormatEx{formatTag: waveFormatPCM, channels: uint16(channels), samplesPerSec: SampleRate,
		avgBytesPerSec: SampleRate * uint32(channels) * 2, blockAlign: uint16(channels) * 2, bitsPerSample: 16}
}

type propertyKey struct {
	fmtid guid
	pid   uint32
}

// propVariant is PROPVARIANT on 64-bit Windows: a type tag, padding, then
// a 16-byte union.
type propVariant struct {
	vt  uint16
	_   [3]uint16
	val uintptr
	ptr unsafe.Pointer
}

// lpwstr is the string of a VT_LPWSTR value, which sits in the union's
// first word.
func (pv *propVariant) lpwstr() *uint16 { return *(**uint16)(unsafe.Pointer(&pv.val)) }

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

// WASAPICapturer records in WASAPI shared mode, converting to 48 kHz int16
// (AUTOCONVERTPCM). For desktop audio it uses process loopback, which
// leaves out Planck's own process tree (Windows 10 build 20348 and later),
// and falls back to loopback of the default render endpoint; as a mic
// capturer it records the default capture endpoint in mono.
type WASAPICapturer struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
	mode   string

	mic      bool
	deviceID string
	opts     CaptureOptions
	// endpointOnly skips process loopback, for tests.
	endpointOnly bool
}

// NewCapturer makes a desktop audio capturer. opts.ExcludePIDs is not
// supported: process loopback excludes one process tree.
func NewCapturer(opts CaptureOptions) (*WASAPICapturer, error) {
	if len(opts.ExcludePIDs) > 0 {
		return nil, fmt.Errorf("audio: excluding more processes: %w (Windows process loopback excludes one process tree, Planck's)", ErrUnsupported)
	}
	return &WASAPICapturer{opts: opts}, nil
}

// NewMicCapturer records the default capture endpoint.
func NewMicCapturer() (MicCapturer, error) { return &WASAPICapturer{mic: true}, nil }

// Mode says which capture path Start opened.
func (c *WASAPICapturer) Mode() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.mode
}

func (c *WASAPICapturer) Start(ctx context.Context, onPCM func(pcm []int16, pts int64)) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.done != nil {
		return errors.New("audio: capture already started")
	}
	ctx, cancel := context.WithCancel(ctx)
	ready := make(chan error, 1)
	done := make(chan struct{})
	var mode string
	go func() {
		defer close(done)
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		c.captureLoop(ctx, onPCM, &mode, ready)
	}()
	if err := <-ready; err != nil {
		cancel()
		<-done
		return err
	}
	c.cancel, c.done, c.mode = cancel, done, mode
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

// comInit joins the thread to the multithreaded apartment.
func comInit() error {
	const coinitMultithreaded = 0
	if r, _, _ := procCoInit.Call(0, coinitMultithreaded); int32(r) < 0 {
		return hresult("CoInitializeEx", r)
	}
	return nil
}

func newEnumerator() (*comObject, error) {
	var enum *comObject
	r := callProc(procCoCreate, uintptr(unsafe.Pointer(&clsidMMDeviceEnumerator)), 0, clsctxAll,
		uintptr(unsafe.Pointer(&iidIMMDeviceEnumerator)), uintptr(unsafe.Pointer(&enum)))
	return enum, hresult("CoCreateInstance", r)
}

// openDevice returns the endpoint with id, or the default endpoint of
// flow (eRender or eCapture) when id is empty.
func openDevice(flow uintptr, id string) (*comObject, error) {
	enum, err := newEnumerator()
	if err != nil {
		return nil, err
	}
	defer enum.release()
	var device *comObject
	if id == "" {
		return device, hresult("GetDefaultAudioEndpoint", enum.call(enumGetDefaultAudioEndpoint, flow, eConsole, uintptr(unsafe.Pointer(&device))))
	}
	p, err := syscall.UTF16PtrFromString(id)
	if err != nil {
		return nil, err
	}
	return device, hresult("GetDevice", enum.call2(enumGetDevice, uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&device))))
}

func activate(device *comObject, iid *guid) (*comObject, error) {
	var obj *comObject
	return obj, hresult("Activate", device.call(deviceActivate, uintptr(unsafe.Pointer(iid)), clsctxAll, 0, uintptr(unsafe.Pointer(&obj))))
}

// endpointClient activates an IAudioClient on openDevice(flow, id).
func endpointClient(flow uintptr, id string) (*comObject, error) {
	device, err := openDevice(flow, id)
	if err != nil {
		return nil, err
	}
	defer device.release()
	return activate(device, &iidIAudioClient)
}

// endpointState is an endpoint's shared-mode mix format and volume.
type endpointState struct {
	channels, rate int
	levelDB        float32
	muted          bool
}

func readEndpoint(id string) (st endpointState, err error) {
	device, err := openDevice(eRender, id)
	if err != nil {
		return st, err
	}
	defer device.release()
	client, err := activate(device, &iidIAudioClient)
	if err != nil {
		return st, err
	}
	defer client.release()
	var f *waveFormatEx
	if err := hresult("GetMixFormat", client.call1(clientGetMixFormat, uintptr(unsafe.Pointer(&f)))); err != nil {
		return st, err
	}
	st.channels, st.rate = int(f.channels), int(f.samplesPerSec)
	procCoTaskFree.Call(uintptr(unsafe.Pointer(f)))
	vol, err := activate(device, &iidEndpointVolume)
	if err != nil {
		return st, err
	}
	defer vol.release()
	var muted int32
	vol.call1(volGetMasterLevel, uintptr(unsafe.Pointer(&st.levelDB)))
	vol.call1(volGetMute, uintptr(unsafe.Pointer(&muted)))
	st.muted = muted != 0
	return st, nil
}

// setMute mutes or unmutes an endpoint.
func setMute(id string, mute bool) error {
	device, err := openDevice(eRender, id)
	if err != nil {
		return err
	}
	defer device.release()
	vol, err := activate(device, &iidEndpointVolume)
	if err != nil {
		return err
	}
	defer vol.release()
	var b uintptr
	if mute {
		b = 1
	}
	return hresult("SetMute", vol.call2(volSetMute, b, 0))
}

func (c *WASAPICapturer) openClient() (client *comObject, mode string, channels int, loopback bool, err error) {
	if c.mic {
		client, err = endpointClient(eCapture, c.deviceID)
		return client, ModeMicrophone, MicChannels, false, err
	}
	if !c.endpointOnly {
		client, err = processLoopbackClient(uint32(os.Getpid()))
	}
	if err == nil && client != nil {
		return client, ModeProcessLoopback, Channels, true, nil
	}
	client, err = endpointClient(eRender, c.deviceID)
	return client, ModeEndpointLoopback, Channels, true, err
}

func (c *WASAPICapturer) captureLoop(ctx context.Context, onPCM func([]int16, int64), mode *string, ready chan<- error) {
	fail := func(err error) { ready <- err }
	if err := comInit(); err != nil {
		fail(err)
		return
	}
	defer procCoUninit.Call()

	client, m, channels, loopback, err := c.openClient()
	if err != nil {
		fail(err)
		return
	}
	defer client.release()
	*mode = m

	flags := uintptr(streamFlagsEvent | streamFlagsAutoConv | streamFlagsSRCDefault)
	if loopback {
		flags |= streamFlagsLoopback
	}
	format := pcmFormat(channels)
	const bufferDuration = 20 * time.Millisecond / 100 // REFERENCE_TIME is 100ns units
	if err := hresult("Initialize", client.call(clientInitialize, shareModeShared, flags,
		uintptr(bufferDuration), 0, uintptr(unsafe.Pointer(format)), 0)); err != nil {
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

	st := &captureState{pcm: make([]int16, SampleRate/10*channels)}
	silence := make([]int16, FrameSamples(DefaultFrameDuration)*channels)
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
			n := int(st.frames) * channels
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
			pts := int64(st.qpcPos) * 100
			if st.qpcPos == 0 {
				pts = qpcNow()
			}
			onPCM(pcm, pts)
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

// activationParams is AUDIOCLIENT_ACTIVATION_PARAMS for process loopback.
type activationParams struct {
	activationType uint32
	targetPID      uint32
	loopbackMode   uint32
}

// completionHandler is a Go-built IActivateAudioInterfaceCompletionHandler.
// It lives on the heap, which does not move, for as long as activation
// can call it.
type completionHandler struct {
	vtbl  *[4]uintptr
	event uintptr
}

var (
	completionVtblOnce sync.Once
	completionVtbl     [4]uintptr
)

func initCompletionVtbl() {
	completionVtbl = [4]uintptr{
		syscall.NewCallback(func(this unsafe.Pointer, riid *guid, ppv *unsafe.Pointer) uintptr {
			switch *riid {
			case iidIUnknown, iidIAgileObject, iidActivateCompletion:
				*ppv = this
				return 0
			}
			*ppv = nil
			return eNoInterface
		}),
		// The handler outlives every reference COM holds, so counting is
		// not needed.
		syscall.NewCallback(func(this uintptr) uintptr { return 1 }),
		syscall.NewCallback(func(this uintptr) uintptr { return 1 }),
		syscall.NewCallback(func(h *completionHandler, op uintptr) uintptr {
			procSetEvent.Call(h.event)
			return 0
		}),
	}
}

// processLoopbackClient activates an IAudioClient that records every
// process except pid and its children.
func processLoopbackClient(pid uint32) (*comObject, error) {
	if err := procActivateAsync.Find(); err != nil {
		return nil, err
	}
	completionVtblOnce.Do(initCompletionVtbl)
	event, _, errno := procCreateEvent.Call(0, 0, 0, 0)
	if event == 0 {
		return nil, fmt.Errorf("audio: CreateEventW: %w", errno)
	}
	timedOut := false
	defer func() {
		if !timedOut {
			syscall.CloseHandle(syscall.Handle(event))
		}
	}()
	// Activation reads the parameters and calls the handler from another
	// thread, so they live in a heap object that a global keeps alive until
	// it completes, or for good if it never does.
	a := &activation{handler: completionHandler{vtbl: &completionVtbl}}
	a.handler.event = event
	a.params = activationParams{activationType: activationTypeProcessLoopback, targetPID: pid, loopbackMode: loopbackModeExcludeTree}
	a.pv = propVariant{vt: vtBlob, val: unsafe.Sizeof(a.params), ptr: unsafe.Pointer(&a.params)}
	activationsMu.Lock()
	activations[a] = struct{}{}
	activationsMu.Unlock()
	path, _ := syscall.UTF16PtrFromString(processLoopbackPath)
	var op *comObject
	r := callProc(procActivateAsync, uintptr(unsafe.Pointer(path)), uintptr(unsafe.Pointer(&iidIAudioClient)),
		uintptr(unsafe.Pointer(&a.pv)), uintptr(unsafe.Pointer(&a.handler)), uintptr(unsafe.Pointer(&op)))
	if err := hresult("ActivateAudioInterfaceAsync", r); err != nil {
		return nil, err
	}
	defer op.release()
	const waitTimeout = 0x102
	if w, _ := syscall.WaitForSingleObject(syscall.Handle(event), 5000); w == waitTimeout {
		timedOut = true
		return nil, errors.New("audio: process loopback activation timed out")
	}
	activationsMu.Lock()
	delete(activations, a)
	activationsMu.Unlock()
	var activated uint32
	var client *comObject
	if err := hresult("GetActivateResult", op.call2(activateOpGetResult, uintptr(unsafe.Pointer(&activated)), uintptr(unsafe.Pointer(&client)))); err != nil {
		return nil, err
	}
	if err := hresult("process loopback activation", uintptr(activated)); err != nil {
		return nil, err
	}
	return client, nil
}

type activation struct {
	handler completionHandler
	params  activationParams
	pv      propVariant
}

var (
	activationsMu sync.Mutex
	activations   = map[*activation]struct{}{}
)

// outputDevices lists the active render endpoints by friendly name.
func outputDevices() ([]VirtualMic, error) { return endpoints(eRender) }

func endpoints(flow uintptr) ([]VirtualMic, error) {
	var out []VirtualMic
	err := onCOMThread(func() error {
		enum, err := newEnumerator()
		if err != nil {
			return err
		}
		defer enum.release()
		var coll *comObject
		if err := hresult("EnumAudioEndpoints", enum.call(enumEnumAudioEndpoints, flow, deviceStateActive, uintptr(unsafe.Pointer(&coll)))); err != nil {
			return err
		}
		defer coll.release()
		var count uint32
		if err := hresult("GetCount", coll.call1(collGetCount, uintptr(unsafe.Pointer(&count)))); err != nil {
			return err
		}
		for i := range count {
			var dev *comObject
			if hresult("Item", coll.call2(collItem, uintptr(i), uintptr(unsafe.Pointer(&dev)))) != nil {
				continue
			}
			id, name := deviceIDAndName(dev)
			dev.release()
			if id != "" {
				out = append(out, VirtualMic{ID: id, Name: name})
			}
		}
		return nil
	})
	return out, err
}

func deviceIDAndName(dev *comObject) (id, name string) {
	var p *uint16
	if hresult("GetId", dev.call1(deviceGetID, uintptr(unsafe.Pointer(&p)))) == nil && p != nil {
		id = windows.UTF16PtrToString(p)
		procCoTaskFree.Call(uintptr(unsafe.Pointer(p)))
	}
	var props *comObject
	if hresult("OpenPropertyStore", dev.call2(deviceOpenPropertyStore, stgmRead, uintptr(unsafe.Pointer(&props)))) != nil {
		return id, ""
	}
	defer props.release()
	key := pkeyDeviceFriendlyName
	var pv propVariant
	if hresult("GetValue", props.call2(propsGetValue, uintptr(unsafe.Pointer(&key)), uintptr(unsafe.Pointer(&pv)))) == nil {
		if pv.vt == vtLPWSTR && pv.val != 0 {
			name = windows.UTF16PtrToString(pv.lpwstr())
		}
		procPropVarClear.Call(uintptr(unsafe.Pointer(&pv)))
	}
	return id, name
}

// onCOMThread runs fn on a locked thread in the multithreaded apartment.
func onCOMThread(fn func() error) error {
	errc := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		if err := comInit(); err != nil {
			errc <- err
			return
		}
		defer procCoUninit.Call()
		errc <- fn()
	}()
	return <-errc
}

// prepareVirtualMic finds the input side of dev and its volume, and checks
// that Steam Streaming Microphone passes audio: it copies bytes from its
// playback side to its recording side without converting, so the two need
// the same format.
func prepareVirtualMic(dev *VirtualMic) error {
	inputs, err := endpoints(eCapture)
	if err != nil {
		return err
	}
	names := make([]string, len(inputs))
	for i, d := range inputs {
		names[i] = d.Name
	}
	i := pairedInput(dev.Name, names)
	if i < 0 {
		return nil
	}
	dev.Input = inputs[i].Name
	return onCOMThread(func() error {
		out, err := readEndpoint(dev.ID)
		if err != nil {
			return err
		}
		in, err := readEndpoint(inputs[i].ID)
		if err != nil {
			return err
		}
		dev.inputGainDB = in.levelDB
		if strings.Contains(dev.Name, "Steam Streaming Microphone") && (out.channels != in.channels || out.rate != in.rate) {
			return fmt.Errorf("%w: %q plays %d ch at %d Hz but %q records %d ch at %d Hz; Steam's driver does not convert, so set both to the same format (Sound settings > device Properties > Advanced)",
				ErrVirtualMicSetup, dev.Name, out.channels, out.rate, dev.Input, in.channels, in.rate)
		}
		return nil
	})
}

// wasapiSink renders mono PCM into one endpoint as float32, scaled down by
// the input side's gain (Steam Streaming Microphone records at +30 dB), and
// unmutes the endpoint while it is open.
type wasapiSink struct {
	dev    VirtualMic
	ring   *pcmRing
	cancel context.CancelFunc
	done   chan struct{}
	mu     sync.Mutex
	closed bool
}

// OpenMicSink plays mic PCM into the playback side of dev, found with
// FindVirtualMic.
func OpenMicSink(dev VirtualMic) (MicSink, error) {
	s := &wasapiSink{dev: dev, ring: newPCMRing(samplesForChannels(60*time.Millisecond, MicChannels)), done: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	ready := make(chan error, 1)
	go func() {
		defer close(s.done)
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		s.renderLoop(ctx, ready)
	}()
	if err := <-ready; err != nil {
		cancel()
		<-s.done
		return nil, fmt.Errorf("audio: open %s: %w", dev.Name, err)
	}
	return s, nil
}

func (s *wasapiSink) Device() string { return s.dev.Input }

func (s *wasapiSink) Write(pcm []int16) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	s.ring.write(pcm)
	return nil
}

func (s *wasapiSink) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	s.cancel()
	<-s.done
	return nil
}

func (s *wasapiSink) renderLoop(ctx context.Context, ready chan<- error) {
	fail := func(err error) { ready <- err }
	if err := comInit(); err != nil {
		fail(err)
		return
	}
	defer procCoUninit.Call()
	if st, err := readEndpoint(s.dev.ID); err == nil && st.muted {
		if setMute(s.dev.ID, false) == nil {
			defer setMute(s.dev.ID, true)
		}
	}
	gain := float32(1)
	if s.dev.inputGainDB > 0 {
		gain = float32(math.Pow(10, -float64(s.dev.inputGainDB)/20))
	}
	client, err := endpointClient(eRender, s.dev.ID)
	if err != nil {
		fail(err)
		return
	}
	defer client.release()
	format := &waveFormatEx{formatTag: waveFormatFloat, channels: MicChannels, samplesPerSec: SampleRate,
		avgBytesPerSec: SampleRate * MicChannels * 4, blockAlign: MicChannels * 4, bitsPerSample: 32}
	const bufferDuration = 20 * time.Millisecond / 100
	if err := hresult("Initialize", client.call(clientInitialize, shareModeShared, streamFlagsEvent|streamFlagsAutoConv|streamFlagsSRCDefault,
		uintptr(bufferDuration), 0, uintptr(unsafe.Pointer(format)), 0)); err != nil {
		fail(err)
		return
	}
	var bufFrames uint32
	if err := hresult("GetBufferSize", client.call1(clientGetBufferSize, uintptr(unsafe.Pointer(&bufFrames)))); err != nil {
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
	var render *comObject
	if err := hresult("GetService", client.call(clientGetService, uintptr(unsafe.Pointer(&iidIAudioRenderClient)), uintptr(unsafe.Pointer(&render)))); err != nil {
		fail(err)
		return
	}
	defer render.release()
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

	var padding uint32
	var data unsafe.Pointer
	scratch := make([]byte, int(bufFrames)*MicChannels*2)
	for ctx.Err() == nil {
		syscall.WaitForSingleObject(syscall.Handle(event), 10)
		if hresult("GetCurrentPadding", client.call1(clientGetCurrentPadding, uintptr(unsafe.Pointer(&padding)))) != nil {
			continue
		}
		s.ring.mu.Lock()
		queued := uint32(s.ring.n / MicChannels)
		s.ring.mu.Unlock()
		frames := min(bufFrames-padding, queued)
		if frames == 0 {
			continue
		}
		if hresult("GetBuffer", render.call2(renderGetBuffer, uintptr(frames), uintptr(unsafe.Pointer(&data)))) != nil {
			continue
		}
		n := s.ring.read(scratch[:int(frames)*MicChannels*2]) / 2
		out := unsafe.Slice((*float32)(data), int(frames)*MicChannels)
		for i := range n {
			out[i] = float32(int16(binary.LittleEndian.Uint16(scratch[2*i:]))) / 32768 * gain
		}
		clear(out[n:])
		render.call2(renderReleaseBuffer, uintptr(frames), 0)
	}
}

func samplesForChannels(d time.Duration, channels int) int {
	return int(int64(SampleRate)*int64(d)/int64(time.Second)) * channels
}

var (
	_ Capturer    = (*WASAPICapturer)(nil)
	_ MicCapturer = (*WASAPICapturer)(nil)
	_ MicSink     = (*wasapiSink)(nil)
)
