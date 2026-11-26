package gamepad

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/planckdotgg/planck/internal/linux/evdev"
)

// scanInterval bounds how often Controllers lists /dev/input, so a UI that
// polls it does not open every event node each time.
const scanInterval = 500 * time.Millisecond

// inputDir holds the evdev nodes; tests point it elsewhere.
var inputDir = "/dev/input"

// Capture reads game controllers from /dev/input/event* nodes. It finds
// controllers plugged in later when Controllers is called.
type Capture struct {
	mu       sync.Mutex
	closed   bool
	pads     []*evdevPad
	skipped  map[string]struct{}
	nextID   int
	lastScan time.Time
}

type evdevPad struct {
	path      string
	fd        int
	caps      evdevCaps
	layout    evdevLayout
	info      ControllerInfo
	bindings  [NumInputs]Binding
	keys      []byte
	abs       [evdev.AbsMax + 1]int32
	canRumble bool
	effectID  int16
	playing   bool
}

// Open starts reading controllers.
func Open() (*Capture, error) {
	c := &Capture{skipped: map[string]struct{}{}}
	c.scan()
	return c, nil
}

func (c *Capture) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	for _, p := range c.pads {
		p.close()
	}
	c.pads = nil
	return nil
}

func (p *evdevPad) close() { unix.Close(p.fd) }

// Controllers lists the attached controllers in the order they attached.
func (c *Capture) Controllers() []ControllerInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	if time.Since(c.lastScan) >= scanInterval {
		c.scan()
	}
	out := make([]ControllerInfo, len(c.pads))
	for i, p := range c.pads {
		out[i] = p.info
		out[i].Slot = i
	}
	return out
}

// scan opens controllers that appeared and forgets nodes that went away.
func (c *Capture) scan() {
	c.lastScan = time.Now()
	paths, _ := filepath.Glob(filepath.Join(inputDir, "event*"))
	present := map[string]bool{}
	for _, p := range paths {
		present[p] = true
	}
	for p := range c.skipped {
		if !present[p] {
			delete(c.skipped, p)
		}
	}
	kept := c.pads[:0]
	for _, p := range c.pads {
		if present[p.path] {
			kept = append(kept, p)
		} else {
			p.close()
		}
	}
	clear(c.pads[len(kept):])
	c.pads = kept
	for _, path := range paths {
		if _, skip := c.skipped[path]; skip || c.has(path) {
			continue
		}
		pad, err := openEvdevPad(path)
		if err != nil {
			if !errors.Is(err, os.ErrPermission) {
				c.skipped[path] = struct{}{}
			}
			continue
		}
		c.nextID++
		pad.info = pad.caps.info(c.nextID)
		c.pads = append(c.pads, pad)
	}
}

func (c *Capture) has(path string) bool {
	for _, p := range c.pads {
		if p.path == path {
			return true
		}
	}
	return false
}

var errNotGamepad = errors.New("gamepad: not a game controller")

func openEvdevPad(path string) (*evdevPad, error) {
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	canWrite := err == nil
	if err != nil {
		fd, err = unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	}
	if err != nil {
		return nil, err
	}
	p := &evdevPad{path: path, fd: fd, effectID: -1}
	if err := p.readCaps(); err != nil || !p.caps.isGamepad() || strings.HasPrefix(p.caps.Name, virtualPadPrefix) {
		unix.Close(fd)
		if err == nil {
			err = errNotGamepad
		}
		return nil, err
	}
	p.canRumble = canWrite && p.caps.Rumble
	p.layout = p.caps.layout()
	p.bindings = p.layout.bindings(p.caps.Vendor)
	p.keys = make([]byte, evdev.KeyMax/8+1)
	if err := p.resync(); err != nil {
		unix.Close(fd)
		return nil, err
	}
	return p, nil
}

func (p *evdevPad) readCaps() error {
	var name [256]byte
	if err := evdev.Ioctl(p.fd, evdev.EVIOCGNAME(len(name)), unsafe.Pointer(&name[0])); err != nil {
		return err
	}
	p.caps.Name = strings.TrimRight(string(name[:]), "\x00")
	var id [4]uint16
	if err := evdev.Ioctl(p.fd, evdev.EVIOCGID, unsafe.Pointer(&id[0])); err != nil {
		return err
	}
	p.caps.Bus, p.caps.Vendor, p.caps.Product, p.caps.Version = id[0], id[1], id[2], id[3]
	types := make([]byte, 4)
	if err := evdev.Ioctl(p.fd, evdev.EVIOCGBIT(0, len(types)), unsafe.Pointer(&types[0])); err != nil {
		return err
	}
	if evdev.TestBit(types, evdev.EvKey) {
		keys := make([]byte, evdev.KeyMax/8+1)
		if err := evdev.Ioctl(p.fd, evdev.EVIOCGBIT(evdev.EvKey, len(keys)), unsafe.Pointer(&keys[0])); err != nil {
			return err
		}
		for code := 0; code <= evdev.KeyMax; code++ {
			if evdev.TestBit(keys, code) {
				p.caps.Keys = append(p.caps.Keys, uint16(code))
			}
		}
	}
	if evdev.TestBit(types, evdev.EvAbs) {
		abs := make([]byte, evdev.AbsMax/8+1)
		if err := evdev.Ioctl(p.fd, evdev.EVIOCGBIT(evdev.EvAbs, len(abs)), unsafe.Pointer(&abs[0])); err != nil {
			return err
		}
		for code := 0; code <= evdev.AbsMax; code++ {
			if !evdev.TestBit(abs, code) {
				continue
			}
			var info [6]int32
			if err := evdev.Ioctl(p.fd, evdev.EVIOCGABS(code), unsafe.Pointer(&info[0])); err != nil {
				return err
			}
			p.caps.Abs = append(p.caps.Abs, evdevAxis{Code: uint16(code), Min: info[1], Max: info[2]})
			p.abs[code] = info[0]
		}
	}
	if evdev.TestBit(types, evdev.EvFF) {
		ff := make([]byte, 16)
		if err := evdev.Ioctl(p.fd, evdev.EVIOCGBIT(evdev.EvFF, len(ff)), unsafe.Pointer(&ff[0])); err == nil {
			p.caps.Rumble = evdev.TestBit(ff, evdev.FFRumble)
		}
	}
	return nil
}

// resync reads the pressed buttons and axis values from the kernel, after
// opening a device and after events were dropped.
func (p *evdevPad) resync() error {
	if err := evdev.Ioctl(p.fd, evdev.EVIOCGKEY(len(p.keys)), unsafe.Pointer(&p.keys[0])); err != nil {
		return err
	}
	for _, a := range p.caps.Abs {
		var info [6]int32
		if err := evdev.Ioctl(p.fd, evdev.EVIOCGABS(int(a.Code)), unsafe.Pointer(&info[0])); err != nil {
			return err
		}
		p.abs[a.Code] = info[0]
	}
	return nil
}

// drain applies the events the device queued since the last call.
func (p *evdevPad) drain() error {
	var buf [evdev.EventSize * 64]byte
	for {
		n, err := unix.Read(p.fd, buf[:])
		if err != nil {
			switch err {
			case unix.EAGAIN:
				return nil
			case unix.EINTR:
				continue
			case unix.ENODEV:
				return ErrNotConnected
			}
			return fmt.Errorf("gamepad: read %s: %w", p.path, err)
		}
		if n <= 0 {
			return ErrNotConnected
		}
		for off := 0; off+evdev.EventSize <= n; off += evdev.EventSize {
			ev := evdev.ParseEvent(buf[off:])
			switch ev.Type {
			case evdev.EvKey:
				if int(ev.Code) <= evdev.KeyMax {
					if ev.Value != 0 {
						p.keys[ev.Code/8] |= 1 << (ev.Code % 8)
					} else {
						p.keys[ev.Code/8] &^= 1 << (ev.Code % 8)
					}
				}
			case evdev.EvAbs:
				if ev.Code <= evdev.AbsMax {
					p.abs[ev.Code] = ev.Value
				}
			case evdev.EvSyn:
				if ev.Code == evdev.SynDrop {
					if err := p.resync(); err != nil {
						return err
					}
				}
			}
		}
	}
}

func (c *Capture) pad(id int) *evdevPad {
	for _, p := range c.pads {
		if p.info.ID == id {
			return p
		}
	}
	return nil
}

// drop forgets a controller that went away.
func (c *Capture) drop(p *evdevPad) {
	for i, q := range c.pads {
		if q == p {
			c.pads = append(c.pads[:i], c.pads[i+1:]...)
			p.close()
			return
		}
	}
}

// DefaultMapping is controller id's standard mapping, from the kernel's
// gamepad button and axis codes.
func (c *Capture) DefaultMapping(id int) Mapping {
	c.mu.Lock()
	defer c.mu.Unlock()
	p := c.pad(id)
	if p == nil {
		return DefaultMapping("", "")
	}
	b := p.bindings
	return Mapping{GUID: p.info.GUID, Name: p.info.Name, Bindings: b, defaults: &b}
}

// Read fills raw with controller id's inputs, indexed as SDL indexes them.
func (c *Capture) Read(id int, raw *RawState) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	p := c.pad(id)
	if p == nil {
		return ErrNotConnected
	}
	if err := p.drain(); err != nil {
		if errors.Is(err, ErrNotConnected) {
			c.drop(p)
		}
		return err
	}
	p.layout.fill(raw,
		func(code uint16) bool { return p.keys[code/8]&(1<<(code%8)) != 0 },
		func(code uint16) int32 { return p.abs[code] })
	return nil
}

// Rumble sets the controller's motors, 0..255 each: large is the strong
// motor and small the weak one. Zero for both stops it.
func (c *Capture) Rumble(id int, large, small uint8) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	p := c.pad(id)
	if p == nil {
		return ErrNotConnected
	}
	if !p.canRumble {
		return fmt.Errorf("%w: %s has no rumble, or /dev/input is read-only for this user", ErrUnsupported, p.info.Name)
	}
	if large == 0 && small == 0 {
		if !p.playing {
			return nil
		}
		p.playing = false
		return p.play(0)
	}
	eff := ffEffectBytes(p.effectID, motor(large), motor(small))
	if err := evdev.Ioctl(p.fd, evdev.EVIOCSFF, unsafe.Pointer(&eff[0])); err != nil {
		return fmt.Errorf("gamepad: upload rumble: %w", err)
	}
	p.effectID = ffEffectID(eff)
	p.playing = true
	return p.play(1)
}

func (p *evdevPad) play(value int32) error {
	b := evdev.Encode(nil, evdev.Event{Type: evdev.EvFF, Code: uint16(p.effectID), Value: value})
	if _, err := unix.Write(p.fd, b); err != nil {
		return fmt.Errorf("gamepad: play rumble: %w", err)
	}
	return nil
}
