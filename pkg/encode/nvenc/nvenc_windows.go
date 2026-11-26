//go:build windows

package nvenc

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/planckdotgg/planck/internal/win"
	"github.com/planckdotgg/planck/pkg/capture"
	"github.com/planckdotgg/planck/pkg/encode"
	"github.com/planckdotgg/planck/pkg/gpu"
)

var nvencDLL = syscall.NewLazyDLL("nvEncodeAPI64.dll")

// asyncEvents: Windows NVENC signals a finished picture on an event.
const asyncEvents = true

// platform is the completion event of an asynchronous encoder.
type platform struct {
	event windows.Handle
}

func (platform) close() {}

func entryPoints() (getMax, create uintptr, err error) {
	if err := nvencDLL.Load(); err != nil {
		return 0, 0, err
	}
	return nvencDLL.NewProc("NvEncodeAPIGetMaxSupportedVersion").Addr(), nvencDLL.NewProc("NvEncodeAPICreateInstance").Addr(), nil
}

//go:uintptrescapes
func ccall(fn uintptr, args ...uintptr) uintptr {
	r, _, _ := syscall.SyscallN(fn, args...)
	return r
}

// ccall2 is ccall for the per-frame path, free of allocations. Pointers
// passed must be to the heap.
func ccall2(fn, a, b uintptr) uintptr {
	r, _, _ := syscall.SyscallN(fn, a, b)
	return r
}

var procSwitchToThread = windows.NewLazySystemDLL("kernel32.dll").NewProc("SwitchToThread")

func yieldThread() { syscall.SyscallN(procSwitchToThread.Addr()) }

// Probe opens a session on device and reports what NVENC offers there.
func Probe(device win.Device, adapter gpu.Adapter) (encode.Caps, error) {
	return probe(deviceTypeDirectX, uintptr(unsafe.Pointer(device.Object)), adapter)
}

// Open starts an encoder on device. Frames passed to Encode must be
// ID3D11Texture2D surfaces created on the same device.
func Open(device win.Device, adapter gpu.Adapter, cfg encode.Config, opts Options) (*Encoder, error) {
	return openEncoder(deviceTypeDirectX, uintptr(unsafe.Pointer(device.Object)), adapter, cfg, opts, platform{})
}

func (e *Encoder) startEvents() error {
	ev, err := windows.CreateEvent(nil, 0, 0, nil)
	if err != nil {
		return err
	}
	e.plat.event = ev
	evp := make([]byte, sizeEventParams)
	put32(evp, 0, verEventParams)
	put64(evp, evEvent, uint64(ev))
	return e.call(fnRegisterAsyncEvent, e.enc, uintptr(unsafe.Pointer(&evp[0])))
}

func (e *Encoder) stopEvents() {
	if e.plat.event == 0 {
		return
	}
	evp := make([]byte, sizeEventParams)
	put32(evp, 0, verEventParams)
	put64(evp, evEvent, uint64(e.plat.event))
	ccall(e.api.fn(fnUnregisterAsyncEvent), e.enc, uintptr(unsafe.Pointer(&evp[0])))
	windows.CloseHandle(e.plat.event)
	e.plat.event = 0
}

func (e *Encoder) waitDone() error {
	ev, err := windows.WaitForSingleObject(e.plat.event, 2000)
	if err != nil || ev != windows.WAIT_OBJECT_0 {
		return fmt.Errorf("nvenc: frame not done in 2 s (wait=%d err=%v)", ev, err)
	}
	return nil
}

// input registers the frame's D3D11 texture once and encodes from it in
// place.
func (e *Encoder) input(f capture.Frame) (registration, error) {
	if r, ok := e.registered[f.Surface]; ok {
		return r, nil
	}
	format, err := bufferFormat(f.Format)
	if err != nil {
		return registration{}, err
	}
	r, err := e.registerResource(resourceDirectX, f.Surface, f.Width, f.Height, uint32(f.Width), format)
	if err != nil {
		return registration{}, err
	}
	e.registered[f.Surface] = r
	return r, nil
}
