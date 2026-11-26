package uinput

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/planckdotgg/planck/internal/linux/evdev"
)

// DevicePath is the uinput node.
const DevicePath = "/dev/uinput"

// RumbleFunc receives a game's rumble on the device, strengths 0..65535;
// both zero stops it.
type RumbleFunc func(strong, weak uint16)

// Device is a created virtual device. Emit and Close are safe from any
// goroutine.
type Device struct {
	fd   int
	file *os.File

	mu     sync.Mutex
	closed bool
	buf    []byte

	onRumble RumbleFunc
	effects  map[int16][2]uint16
	done     chan struct{}
}

// Available reports whether DevicePath can be opened for writing.
func Available() error {
	fd, err := unix.Open(DevicePath, unix.O_WRONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("uinput: %w", err)
	}
	return unix.Close(fd)
}

// Create makes the device. onRumble may be nil; it is called from a
// goroutine of the device's own.
func Create(cfg Config, onRumble RumbleFunc) (*Device, error) {
	fd, err := unix.Open(DevicePath, unix.O_RDWR|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("uinput: %w", err)
	}
	d := &Device{fd: fd, onRumble: onRumble, effects: map[int16][2]uint16{}}
	if err := d.configure(&cfg); err != nil {
		unix.Close(fd)
		return nil, err
	}
	d.file = os.NewFile(uintptr(fd), DevicePath)
	if cfg.Rumble {
		d.done = make(chan struct{})
		go d.serveFF()
	}
	return d, nil
}

func (d *Device) setBit(req uintptr, bit uint16) error {
	return evdev.IoctlInt(d.fd, req, uintptr(bit))
}

func (d *Device) configure(cfg *Config) error {
	evs := map[uint16]bool{evdev.EvSyn: true}
	if len(cfg.Keys) > 0 {
		evs[evdev.EvKey] = true
	}
	if len(cfg.Rels) > 0 {
		evs[evdev.EvRel] = true
	}
	if len(cfg.Abs) > 0 {
		evs[evdev.EvAbs] = true
	}
	if cfg.Rumble {
		evs[evdev.EvFF] = true
	}
	for ev := range evs {
		if err := d.setBit(ioctlSetEvBit, ev); err != nil {
			return fmt.Errorf("uinput: UI_SET_EVBIT %d: %w", ev, err)
		}
	}
	for _, k := range cfg.Keys {
		if err := d.setBit(ioctlSetKeyBit, k); err != nil {
			return fmt.Errorf("uinput: UI_SET_KEYBIT %d: %w", k, err)
		}
	}
	for _, r := range cfg.Rels {
		if err := d.setBit(ioctlSetRelBit, r); err != nil {
			return fmt.Errorf("uinput: UI_SET_RELBIT %d: %w", r, err)
		}
	}
	for _, p := range cfg.Props {
		if err := d.setBit(ioctlSetPropBit, p); err != nil {
			return fmt.Errorf("uinput: UI_SET_PROPBIT %d: %w", p, err)
		}
	}
	if cfg.Rumble {
		if err := d.setBit(ioctlSetFFBit, evdev.FFRumble); err != nil {
			return fmt.Errorf("uinput: UI_SET_FFBIT: %w", err)
		}
		if err := d.setBit(ioctlSetFFBit, evdev.FFGain); err != nil {
			return fmt.Errorf("uinput: UI_SET_FFBIT: %w", err)
		}
	}
	for _, a := range cfg.Abs {
		if err := d.setBit(ioctlSetAbsBit, a.Code); err != nil {
			return fmt.Errorf("uinput: UI_SET_ABSBIT %d: %w", a.Code, err)
		}
		b := absSetupBytes(a)
		if err := evdev.Ioctl(d.fd, ioctlAbsSetup, unsafe.Pointer(&b[0])); err != nil {
			return fmt.Errorf("uinput: UI_ABS_SETUP %d: %w", a.Code, err)
		}
	}
	b := cfg.setupBytes()
	if err := evdev.Ioctl(d.fd, ioctlDevSetup, unsafe.Pointer(&b[0])); err != nil {
		return fmt.Errorf("uinput: UI_DEV_SETUP: %w", err)
	}
	if err := evdev.Ioctl(d.fd, ioctlDevCreate, nil); err != nil {
		return fmt.Errorf("uinput: UI_DEV_CREATE: %w", err)
	}
	return nil
}

var errClosed = errors.New("uinput: device closed")

// Emit writes evs followed by a SYN_REPORT in one write, so a reader sees
// them as one frame.
func (d *Device) Emit(evs ...evdev.Event) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return errClosed
	}
	d.buf = evdev.Encode(d.buf[:0], evs...)
	d.buf = evdev.Encode(d.buf, evdev.Event{Type: evdev.EvSyn, Code: evdev.SynReport})
	_, err := d.file.Write(d.buf)
	return err
}

// Close destroys the device.
func (d *Device) Close() error {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil
	}
	d.closed = true
	err := evdev.Ioctl(d.fd, ioctlDevDestroy, nil)
	d.mu.Unlock()
	if cerr := d.file.Close(); err == nil {
		err = cerr
	}
	if d.done != nil {
		<-d.done
	}
	return err
}
