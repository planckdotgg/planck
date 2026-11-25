//go:build darwin

package gamepad

import (
	"fmt"
	"runtime"
	"sync"

	"github.com/ebitengine/purego"

	"github.com/planckdotgg/planck/internal/apple"
)

var (
	iokitOnce sync.Once
	iokitErr  error

	ioHIDManagerCreate                    func(alloc uintptr, opts uint32) uintptr
	ioHIDManagerSetDeviceMatchingMultiple func(mgr, matches uintptr)
	ioHIDManagerScheduleWithRunLoop       func(mgr, runLoop, mode uintptr)
	ioHIDManagerUnscheduleFromRunLoop     func(mgr, runLoop, mode uintptr)
	ioHIDManagerOpen                      func(mgr uintptr, opts uint32) int32
	ioHIDManagerClose                     func(mgr uintptr, opts uint32) int32
	ioHIDManagerCopyDevices               func(mgr uintptr) uintptr
	ioHIDDeviceGetProperty                func(dev, key uintptr) uintptr
	ioHIDDeviceCopyMatchingElements       func(dev, match uintptr, opts uint32) uintptr
	ioHIDDeviceGetValue                   func(dev, elem uintptr, value *uintptr) int32
	ioHIDDeviceSetReport                  func(dev uintptr, kind int32, id int, report *byte, n int) int32
	ioHIDElementGetType                   func(elem uintptr) uint32
	ioHIDElementGetUsagePage              func(elem uintptr) uint32
	ioHIDElementGetUsage                  func(elem uintptr) uint32
	ioHIDElementGetLogicalMin             func(elem uintptr) int
	ioHIDElementGetLogicalMax             func(elem uintptr) int
	ioHIDElementGetCookie                 func(elem uintptr) uint32
	ioHIDValueGetIntegerValue             func(value uintptr) int

	cfSetGetCount       func(set uintptr) int
	cfSetGetValues      func(set uintptr, values *uintptr)
	cfArrayCreate       func(alloc uintptr, values *uintptr, n int, callbacks uintptr) uintptr
	cfRunLoopGetCurrent func() uintptr
	cfRunLoopRunInMode  func(mode uintptr, seconds float64, returnAfterSource bool) int32
	cfGetTypeID         func(cf uintptr) uint
	cfStringGetTypeID   func() uint
	cfNumberGetTypeID   func() uint
	cfStringGetCString  func(s uintptr, buf *byte, n int, enc uint32) bool

	runLoopDefaultMode uintptr
	arrayCallbacks     uintptr
	keys               struct {
		usagePage, usage, product, vendor, productID, version, transport, battery uintptr
	}
)

func loadIOKit() error {
	iokitOnce.Do(func() {
		lib, err := purego.Dlopen("/System/Library/Frameworks/IOKit.framework/IOKit", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			iokitErr = fmt.Errorf("gamepad: IOKit: %w", err)
			return
		}
		for fn, name := range map[any]string{
			&ioHIDManagerCreate: "IOHIDManagerCreate", &ioHIDManagerSetDeviceMatchingMultiple: "IOHIDManagerSetDeviceMatchingMultiple",
			&ioHIDManagerScheduleWithRunLoop: "IOHIDManagerScheduleWithRunLoop", &ioHIDManagerUnscheduleFromRunLoop: "IOHIDManagerUnscheduleFromRunLoop",
			&ioHIDManagerOpen: "IOHIDManagerOpen", &ioHIDManagerClose: "IOHIDManagerClose", &ioHIDManagerCopyDevices: "IOHIDManagerCopyDevices",
			&ioHIDDeviceGetProperty: "IOHIDDeviceGetProperty", &ioHIDDeviceCopyMatchingElements: "IOHIDDeviceCopyMatchingElements",
			&ioHIDDeviceGetValue: "IOHIDDeviceGetValue", &ioHIDDeviceSetReport: "IOHIDDeviceSetReport",
			&ioHIDElementGetType: "IOHIDElementGetType", &ioHIDElementGetUsagePage: "IOHIDElementGetUsagePage",
			&ioHIDElementGetUsage: "IOHIDElementGetUsage", &ioHIDElementGetLogicalMin: "IOHIDElementGetLogicalMin",
			&ioHIDElementGetLogicalMax: "IOHIDElementGetLogicalMax", &ioHIDElementGetCookie: "IOHIDElementGetCookie",
			&ioHIDValueGetIntegerValue: "IOHIDValueGetIntegerValue",
		} {
			apple.Register(fn, lib, name)
		}
		cf := apple.CoreFoundation
		for fn, name := range map[any]string{
			&cfSetGetCount: "CFSetGetCount", &cfSetGetValues: "CFSetGetValues", &cfArrayCreate: "CFArrayCreate",
			&cfRunLoopGetCurrent: "CFRunLoopGetCurrent", &cfRunLoopRunInMode: "CFRunLoopRunInMode",
			&cfGetTypeID: "CFGetTypeID", &cfStringGetTypeID: "CFStringGetTypeID", &cfNumberGetTypeID: "CFNumberGetTypeID",
			&cfStringGetCString: "CFStringGetCString",
		} {
			apple.Register(fn, cf, name)
		}
		runLoopDefaultMode = apple.Const(cf, "kCFRunLoopDefaultMode")
		arrayCallbacks = apple.Sym(cf, "kCFTypeArrayCallBacks")
		keys.usagePage, keys.usage = apple.String("DeviceUsagePage"), apple.String("DeviceUsage")
		keys.product, keys.vendor, keys.productID = apple.String("Product"), apple.String("VendorID"), apple.String("ProductID")
		keys.version, keys.transport, keys.battery = apple.String("VersionNumber"), apple.String("Transport"), apple.String("BatteryPercent")
	})
	return iokitErr
}

const (
	elementTypeInputMisc   = 1
	elementTypeInputButton = 2
	elementTypeInputAxis   = 3
	runLoopHandledSource   = 4
	reportTypeOutput       = 1
	ioReturnNoDevice       = -536870208 // 0xe00002c0
	ioReturnNotAttached    = -536870184 // 0xe00002d8
)

// Capture reads HID gamepads and joysticks through IOHIDManager. Every
// IOKit call runs on one locked thread whose run loop the manager is
// scheduled on, so that it sees controllers come and go.
type Capture struct {
	mu     sync.Mutex
	closed bool
	calls  chan func()
	done   chan struct{}

	mgr    uintptr
	pads   []*hidPad
	nextID int
}

type hidPad struct {
	dev    uintptr
	info   ControllerInfo
	layout hidLayout
}

// Open matches HID devices whose usage is Gamepad or Joystick.
func Open() (*Capture, error) {
	return openHID([][2]uint32{{pageGenericDesktop, usageGamepad}, {pageGenericDesktop, usageJoystick}})
}

func openHID(usages [][2]uint32) (*Capture, error) {
	if err := loadIOKit(); err != nil {
		return nil, err
	}
	c := &Capture{calls: make(chan func()), done: make(chan struct{})}
	ready := make(chan error, 1)
	go c.run(usages, ready)
	if err := <-ready; err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Capture) run(usages [][2]uint32, ready chan<- error) {
	runtime.LockOSThread()
	defer close(c.done)
	mgr := ioHIDManagerCreate(0, 0)
	if mgr == 0 {
		ready <- fmt.Errorf("gamepad: IOHIDManagerCreate failed")
		return
	}
	dicts := make([]uintptr, 0, len(usages))
	for _, u := range usages {
		page, usage := apple.Int(int64(u[0])), apple.Int(int64(u[1]))
		dicts = append(dicts, apple.Dict(keys.usagePage, page, keys.usage, usage))
		apple.CFRelease(page)
		apple.CFRelease(usage)
	}
	matches := cfArrayCreate(0, &dicts[0], len(dicts), arrayCallbacks)
	for _, d := range dicts {
		apple.CFRelease(d)
	}
	ioHIDManagerSetDeviceMatchingMultiple(mgr, matches)
	apple.CFRelease(matches)
	rl := cfRunLoopGetCurrent()
	ioHIDManagerScheduleWithRunLoop(mgr, rl, runLoopDefaultMode)
	// A device that will not open (one another process seized) only fails
	// its own reads.
	ioHIDManagerOpen(mgr, 0)
	c.mgr = mgr
	ready <- nil

	for f := range c.calls {
		for range 64 {
			if cfRunLoopRunInMode(runLoopDefaultMode, 0, true) != runLoopHandledSource {
				break
			}
		}
		f()
	}
	for _, p := range c.pads {
		p.release()
	}
	ioHIDManagerUnscheduleFromRunLoop(mgr, rl, runLoopDefaultMode)
	ioHIDManagerClose(mgr, 0)
	apple.CFRelease(mgr)
}

// do runs f on the IOKit thread; false once the capture is closed.
func (c *Capture) do(f func()) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return false
	}
	done := make(chan struct{})
	c.calls <- func() { f(); close(done) }
	<-done
	return true
}

func (c *Capture) Close() error {
	c.mu.Lock()
	if !c.closed {
		c.closed = true
		close(c.calls)
	}
	c.mu.Unlock()
	<-c.done
	return nil
}

// Controllers lists the attached controllers, in the order they attached.
func (c *Capture) Controllers() []ControllerInfo {
	var out []ControllerInfo
	c.do(func() {
		c.refresh()
		for _, p := range c.pads {
			out = append(out, p.info)
		}
	})
	return out
}

func (c *Capture) refresh() {
	set := ioHIDManagerCopyDevices(c.mgr)
	var devs []uintptr
	if set != 0 {
		devs = make([]uintptr, cfSetGetCount(set))
		if len(devs) > 0 {
			cfSetGetValues(set, &devs[0])
		}
	}
	kept := c.pads[:0]
	for _, p := range c.pads {
		if containsPtr(devs, p.dev) {
			kept = append(kept, p)
		} else {
			p.release()
		}
	}
	clear(c.pads[len(kept):])
	c.pads = kept
	for _, d := range devs {
		if !c.has(d) {
			c.nextID++
			c.pads = append(c.pads, newHIDPad(d, c.nextID))
		}
	}
	for i, p := range c.pads {
		p.info.Slot = i
		if pct, ok := numberProp(p.dev, keys.battery); ok {
			p.info.BatteryPct = int(pct)
		}
	}
	if set != 0 {
		apple.CFRelease(set)
	}
}

func containsPtr(s []uintptr, p uintptr) bool {
	for _, v := range s {
		if v == p {
			return true
		}
	}
	return false
}

func (c *Capture) has(dev uintptr) bool {
	for _, p := range c.pads {
		if p.dev == dev {
			return true
		}
	}
	return false
}

func (c *Capture) pad(id int) *hidPad {
	for _, p := range c.pads {
		if p.info.ID == id {
			return p
		}
	}
	return nil
}

func newHIDPad(dev uintptr, id int) *hidPad {
	apple.CFRetain(dev)
	p := &hidPad{dev: dev, info: ControllerInfo{ID: id, BatteryPct: -1, Connected: true}}
	p.info.Name = stringProp(dev, keys.product)
	vid, _ := numberProp(dev, keys.vendor)
	pid, _ := numberProp(dev, keys.productID)
	ver, _ := numberProp(dev, keys.version)
	p.info.VendorID, p.info.ProductID = uint16(vid), uint16(pid)
	switch stringProp(dev, keys.transport) {
	case "USB":
		p.info.Connection = ConnectionUSB
	case "Bluetooth", "Bluetooth Low Energy":
		p.info.Connection = ConnectionBluetooth
	}
	p.info.GUID = HIDGUID(p.info.Connection, p.info.VendorID, p.info.ProductID, uint16(ver))

	var elems []hidElement
	if arr := ioHIDDeviceCopyMatchingElements(dev, 0, 0); arr != 0 {
		for i := range apple.CFArrayGetCount(arr) {
			e := apple.CFArrayGetValueAtIndex(arr, i)
			switch ioHIDElementGetType(e) {
			case elementTypeInputMisc, elementTypeInputButton, elementTypeInputAxis:
			default:
				continue
			}
			elems = append(elems, hidElement{
				page: ioHIDElementGetUsagePage(e), usage: ioHIDElementGetUsage(e), cookie: ioHIDElementGetCookie(e),
				min: int32(ioHIDElementGetLogicalMin(e)), max: int32(ioHIDElementGetLogicalMax(e)), ref: e,
			})
		}
		p.layout = newHIDLayout(elems)
		for _, l := range [][]hidElement{p.layout.buttons, p.layout.axes, p.layout.hats} {
			for _, e := range l {
				apple.CFRetain(e.ref)
			}
		}
		apple.CFRelease(arr)
	}
	return p
}

func (p *hidPad) release() {
	for _, l := range [][]hidElement{p.layout.buttons, p.layout.axes, p.layout.hats} {
		for _, e := range l {
			apple.CFRelease(e.ref)
		}
	}
	apple.CFRelease(p.dev)
}

func stringProp(dev, key uintptr) string {
	v := ioHIDDeviceGetProperty(dev, key)
	if v == 0 || cfGetTypeID(v) != cfStringGetTypeID() {
		return ""
	}
	var buf [256]byte
	if !cfStringGetCString(v, &buf[0], len(buf), 0x08000100) {
		return ""
	}
	for i, b := range buf {
		if b == 0 {
			return string(buf[:i])
		}
	}
	return ""
}

func numberProp(dev, key uintptr) (int64, bool) {
	v := ioHIDDeviceGetProperty(dev, key)
	if v == 0 || cfGetTypeID(v) != cfNumberGetTypeID() {
		return 0, false
	}
	return apple.NumberInt64(v)
}

// DefaultMapping is controller id's starting mapping: sticks and triggers
// from its axis usages, buttons in XInput order.
func (c *Capture) DefaultMapping(id int) Mapping {
	m := Mapping{Bindings: defaultBindings}
	c.do(func() {
		if p := c.pad(id); p != nil {
			b := hidDefaultBindings(p.layout.axes)
			m = Mapping{GUID: p.info.GUID, Name: p.info.Name, Bindings: b, defaults: &b}
		}
	})
	return m
}

// Read fills raw with controller id's inputs, each kind sorted by HID usage.
func (c *Capture) Read(id int, raw *RawState) error {
	err := ErrNotConnected
	c.do(func() {
		p := c.pad(id)
		if p == nil {
			return
		}
		err = p.read(raw)
	})
	return err
}

func (p *hidPad) value(e hidElement) (int32, error) {
	var v uintptr
	switch r := ioHIDDeviceGetValue(p.dev, e.ref, &v); {
	case r == ioReturnNoDevice || r == ioReturnNotAttached:
		return 0, ErrNotConnected
	case r != 0 || v == 0:
		return 0, fmt.Errorf("gamepad: IOHIDDeviceGetValue: 0x%x", uint32(r))
	}
	return int32(ioHIDValueGetIntegerValue(v)), nil
}

// read leaves an element that gives no value released or centred.
func (p *hidPad) read(raw *RawState) error {
	raw.Buttons, raw.Axes, raw.Hats = raw.Buttons[:0], raw.Axes[:0], raw.Hats[:0]
	for _, e := range p.layout.buttons {
		v, err := p.value(e)
		if err == ErrNotConnected {
			return err
		}
		raw.Buttons = append(raw.Buttons, err == nil && v != 0)
	}
	for _, e := range p.layout.axes {
		v, err := p.value(e)
		if err == ErrNotConnected {
			return err
		}
		if err != nil {
			v = e.min + (e.max-e.min)/2
		}
		raw.Axes = append(raw.Axes, Axis{Value: v, Min: e.min, Max: e.max})
	}
	for _, e := range p.layout.hats {
		v, err := p.value(e)
		if err == ErrNotConnected {
			return err
		}
		var bits uint8
		if err == nil {
			bits = hatBits(v, e.min, e.max)
		}
		raw.Hats = append(raw.Hats, bits)
	}
	return nil
}

// Rumble sets controller id's motors, 0..255 each, on the pads whose
// output report this package knows: wired DualShock 4 and DualSense.
// Others return ErrUnsupported.
func (c *Capture) Rumble(id int, large, small uint8) error {
	err := ErrNotConnected
	c.do(func() {
		p := c.pad(id)
		if p == nil {
			return
		}
		report := rumbleReport(p.info, large, small)
		if report == nil {
			err = ErrUnsupported
			return
		}
		if r := ioHIDDeviceSetReport(p.dev, reportTypeOutput, int(report[0]), &report[0], len(report)); r != 0 {
			err = fmt.Errorf("gamepad: IOHIDDeviceSetReport: 0x%x", uint32(r))
			return
		}
		err = nil
	})
	return err
}
