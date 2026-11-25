//go:build windows

package gamepad

import (
	"errors"
	"fmt"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ViGEmBus's device interface, IOCTLs and request layouts, from
// include/ViGEm/km/BusShared.h and include/ViGEm/Common.h in ViGEmClient.
var vigemInterface = windows.GUID{Data1: 0x96E42B22, Data2: 0xF5E9, Data3: 0x42F8,
	Data4: [8]byte{0xB0, 0x43, 0xED, 0x0F, 0x93, 0x2F, 0x01, 0x4F}}

const vigemDriverName = "Nefarius Virtual Gamepad Emulation Bus"

func vigemIoctl(index, access uint32) uint32 {
	const fileDeviceBusExtender, methodBuffered = 0x2A, 0
	return fileDeviceBusExtender<<16 | access<<14 | index<<2 | methodBuffered
}

const (
	fileReadData  = 1
	fileWriteData = 2
	vigemBase     = 0x801
)

var (
	ioctlPluginTarget     = vigemIoctl(vigemBase+0x000, fileWriteData)
	ioctlUnplugTarget     = vigemIoctl(vigemBase+0x001, fileWriteData)
	ioctlCheckVersion     = vigemIoctl(vigemBase+0x002, fileWriteData)
	ioctlWaitDeviceReady  = vigemIoctl(vigemBase+0x003, fileWriteData)
	ioctlXUSBNotification = vigemIoctl(vigemBase+0x200, fileWriteData|fileReadData)
	ioctlXUSBSubmitReport = vigemIoctl(vigemBase+0x201, fileWriteData)
	ioctlDS4SubmitReport  = vigemIoctl(vigemBase+0x202, fileWriteData)
	ioctlDS4Notification  = vigemIoctl(vigemBase+0x203, fileWriteData)
	vigemTargetXbox360    = uint32(0)
	vigemTargetDualShock4 = uint32(2)
	vigemCommonVersion    = uint32(1)
	vigemMaxSerial        = uint32(255)
)

type vigemPluginTarget struct {
	Size, SerialNo, TargetType uint32
	VendorID, ProductID        uint16
}

type vigemSerial struct{ Size, SerialNo uint32 }

type xusbSubmitReport struct {
	Size, SerialNo uint32
	Buttons        uint16
	LT, RT         uint8
	LX, LY, RX, RY int16
}

type xusbNotification struct {
	Size, SerialNo         uint32
	LargeMotor, SmallMotor uint8
	LedNumber              uint8
	_                      uint8
}

type ds4SubmitReport struct {
	Size, SerialNo uint32
	Report         ds4Report
	_              [2]byte
}

type ds4Notification struct {
	Size, SerialNo         uint32
	SmallMotor, LargeMotor uint8
	Red, Green, Blue       uint8
	_                      [3]byte
}

func vigemPaths() []string {
	paths, err := windows.CM_Get_Device_Interface_List("", &vigemInterface, windows.CM_GET_DEVICE_INTERFACE_LIST_PRESENT)
	if err != nil {
		return nil
	}
	return paths
}

func VirtualPadStatus() PadDriverStatus {
	if len(vigemPaths()) == 0 {
		return PadDriverStatus{
			DriverName:  vigemDriverName,
			InstallHint: "Install ViGEmBus (github.com/nefarius/ViGEmBus/releases) to let clients use gamepads on this machine.",
		}
	}
	return PadDriverStatus{Installed: true, DriverName: vigemDriverName, SupportedTypes: []PadType{PadXbox360, PadDS4}}
}

// OpenVirtualPads opens the ViGEmBus driver. A DualSense request plugs in
// DualShock 4s, which ViGEmBus emulates in its place.
func OpenVirtualPads(t PadType) (*VirtualPads, error) {
	paths := vigemPaths()
	if len(paths) == 0 {
		return nil, fmt.Errorf("%w: ViGEmBus is not installed", ErrUnsupported)
	}
	var errs []error
	for _, p := range paths {
		bus, err := openVigem(p)
		if err == nil {
			return newVirtualPads(bus, t), nil
		}
		errs = append(errs, err)
	}
	return nil, errors.Join(errs...)
}

type vigemBus struct {
	h windows.Handle
}

func openVigem(path string) (*vigemBus, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_NO_BUFFERING|windows.FILE_FLAG_WRITE_THROUGH|windows.FILE_FLAG_OVERLAPPED, 0)
	if err != nil {
		return nil, fmt.Errorf("gamepad: open ViGEmBus: %w", err)
	}
	b := &vigemBus{h: h}
	io, err := newOverlappedIO()
	if err != nil {
		windows.CloseHandle(h)
		return nil, err
	}
	defer io.close()
	ver := &vigemSerial{Size: 8, SerialNo: vigemCommonVersion}
	if err := io.do(h, ioctlCheckVersion, unsafe.Pointer(ver), 8, nil, 0); err != nil {
		windows.CloseHandle(h)
		return nil, fmt.Errorf("gamepad: ViGEmBus version check: %w", err)
	}
	return b, nil
}

func (b *vigemBus) close() error { return windows.CloseHandle(b.h) }

// overlappedIO runs one DeviceIoControl at a time on an OVERLAPPED handle
// and waits for it. The OVERLAPPED lives on the heap, which the kernel may
// write to after the call returns.
type overlappedIO struct {
	ov *windows.Overlapped
}

func newOverlappedIO() (*overlappedIO, error) {
	ev, err := windows.CreateEvent(nil, 0, 0, nil)
	if err != nil {
		return nil, err
	}
	return &overlappedIO{ov: &windows.Overlapped{HEvent: ev}}, nil
}

func (o *overlappedIO) close() { windows.CloseHandle(o.ov.HEvent) }

func (o *overlappedIO) do(h windows.Handle, code uint32, in unsafe.Pointer, inLen uint32, out unsafe.Pointer, outLen uint32) error {
	var n uint32
	err := windows.DeviceIoControl(h, code, (*byte)(in), inLen, (*byte)(out), outLen, &n, o.ov)
	if err != nil && err != windows.ERROR_IO_PENDING {
		return err
	}
	return windows.GetOverlappedResult(h, o.ov, &n, true)
}

func (b *vigemBus) plug(t PadType, onRumble func(large, small uint8)) (padTarget, error) {
	kind, vid, pid := vigemTargetXbox360, uint16(0x045E), uint16(0x028E)
	if t == PadDS4 || t == PadDualSense {
		kind, vid, pid = vigemTargetDualShock4, 0x054C, 0x05C4
	}
	io, err := newOverlappedIO()
	if err != nil {
		return nil, err
	}
	req := &vigemPluginTarget{Size: uint32(unsafe.Sizeof(vigemPluginTarget{})), TargetType: kind, VendorID: vid, ProductID: pid}
	var plugErr error
	for req.SerialNo = 1; req.SerialNo <= vigemMaxSerial; req.SerialNo++ {
		if plugErr = io.do(b.h, ioctlPluginTarget, unsafe.Pointer(req), req.Size, nil, 0); plugErr == nil {
			break
		}
	}
	if plugErr != nil {
		io.close()
		return nil, fmt.Errorf("gamepad: ViGEmBus plug in: %w", plugErr)
	}
	tg := &vigemTarget{bus: b, io: io, serial: req.SerialNo, ds4: kind == vigemTargetDualShock4}
	wait := &vigemSerial{Size: 8, SerialNo: tg.serial}
	// Drivers before v1.17 lack IOCTL_VIGEM_WAIT_DEVICE_READY and answer
	// ERROR_INVALID_PARAMETER; their plug-in already waited.
	if err := io.do(b.h, ioctlWaitDeviceReady, unsafe.Pointer(wait), 8, nil, 0); err != nil && err != windows.ERROR_INVALID_PARAMETER {
		tg.unplug()
		return nil, fmt.Errorf("gamepad: ViGEmBus wait for pad: %w", err)
	}
	if err := tg.watchRumble(onRumble); err != nil {
		tg.unplug()
		return nil, err
	}
	// A new pad takes reports only once its driver stack polls it, some
	// milliseconds after plug-in; until then the bus refuses them and the
	// pad reads back values that were never sent. A neutral report that
	// lands first makes the caller's first state land too.
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); time.Sleep(2 * time.Millisecond) {
		if tg.submitReport(State{}) != windows.ERROR_NO_MORE_ITEMS {
			break
		}
	}
	return tg, nil
}

type vigemTarget struct {
	bus    *vigemBus
	serial uint32
	ds4    bool

	mu      sync.Mutex
	io      *overlappedIO
	xusb    xusbSubmitReport
	ds4Rep  ds4SubmitReport
	notify  *overlappedIO
	plugged bool
}

// submit drops a report the pad has no read pending for
// (ERROR_NO_MORE_ITEMS), as ViGEmClient does; states are absolute, so the
// next one carries it.
func (t *vigemTarget) submit(s State) error {
	err := t.submitReport(s)
	if err == windows.ERROR_NO_MORE_ITEMS {
		return nil
	}
	return err
}

func (t *vigemTarget) submitReport(s State) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.ds4 {
		t.ds4Rep = ds4SubmitReport{Size: uint32(unsafe.Sizeof(t.ds4Rep)), SerialNo: t.serial, Report: toDS4(s)}
		return t.io.do(t.bus.h, ioctlDS4SubmitReport, unsafe.Pointer(&t.ds4Rep), t.ds4Rep.Size, nil, 0)
	}
	t.xusb = xusbSubmitReport{Size: uint32(unsafe.Sizeof(t.xusb)), SerialNo: t.serial,
		Buttons: uint16(s.Buttons), LT: s.LT, RT: s.RT, LX: s.LX, LY: s.LY, RX: s.RX, RY: s.RY}
	return t.io.do(t.bus.h, ioctlXUSBSubmitReport, unsafe.Pointer(&t.xusb), t.xusb.Size, nil, 0)
}

// watchRumble keeps one notification request pending on the driver; it
// completes each time a game sets the motors or the LED, until the pad is
// unplugged. Only motor changes reach onRumble.
func (t *vigemTarget) watchRumble(onRumble func(large, small uint8)) error {
	n, err := newOverlappedIO()
	if err != nil {
		return err
	}
	t.notify, t.plugged = n, true
	go func() {
		defer n.close()
		var last [2]uint8
		report := func(large, small uint8) {
			if m := [2]uint8{large, small}; m != last {
				last = m
				onRumble(large, small)
			}
		}
		if t.ds4 {
			req := &ds4Notification{}
			for {
				req.Size, req.SerialNo = uint32(unsafe.Sizeof(*req)), t.serial
				if err := n.do(t.bus.h, ioctlDS4Notification, unsafe.Pointer(req), req.Size, unsafe.Pointer(req), req.Size); err != nil {
					return
				}
				report(req.LargeMotor, req.SmallMotor)
			}
		}
		req := &xusbNotification{}
		for {
			req.Size, req.SerialNo = uint32(unsafe.Sizeof(*req)), t.serial
			if err := n.do(t.bus.h, ioctlXUSBNotification, unsafe.Pointer(req), req.Size, unsafe.Pointer(req), req.Size); err != nil {
				return
			}
			report(req.LargeMotor, req.SmallMotor)
		}
	}()
	return nil
}

func (t *vigemTarget) unplug() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.notify != nil {
		windows.CancelIoEx(t.bus.h, t.notify.ov)
		t.notify = nil
	}
	req := &vigemSerial{Size: 8, SerialNo: t.serial}
	err := t.io.do(t.bus.h, ioctlUnplugTarget, unsafe.Pointer(req), 8, nil, 0)
	t.io.close()
	if err != nil {
		return fmt.Errorf("gamepad: ViGEmBus unplug %d: %w", t.serial, err)
	}
	return nil
}
