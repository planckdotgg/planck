package uinput

import (
	"unsafe"

	"github.com/planckdotgg/planck/internal/linux/evdev"
)

func (d *Device) serveFF() {
	defer close(d.done)
	var buf [evdev.EventSize * 16]byte
	for {
		n, err := d.file.Read(buf[:])
		if err != nil {
			return
		}
		for off := 0; off+evdev.EventSize <= n; off += evdev.EventSize {
			d.handleFF(evdev.ParseEvent(buf[off:]))
		}
	}
}

func (d *Device) handleFF(ev evdev.Event) {
	switch ev.Type {
	case evdev.EvUinput:
		switch ev.Code {
		case uiFFUpload:
			var up [ffUploadSize]byte
			putRequest(up[:], uint32(ev.Value))
			if evdev.Ioctl(d.fd, ioctlBeginFFUpload, unsafe.Pointer(&up[0])) != nil {
				return
			}
			if id, strong, weak, ok := parseUpload(up[:]); ok {
				d.mu.Lock()
				d.effects[id] = [2]uint16{strong, weak}
				d.mu.Unlock()
			} else {
				putRetval(up[:], -22)
			}
			evdev.Ioctl(d.fd, ioctlEndFFUpload, unsafe.Pointer(&up[0]))
		case uiFFErase:
			var er [ffEraseSize]byte
			putRequest(er[:], uint32(ev.Value))
			if evdev.Ioctl(d.fd, ioctlBeginFFErase, unsafe.Pointer(&er[0])) != nil {
				return
			}
			d.mu.Lock()
			delete(d.effects, int16(eraseID(er[:])))
			d.mu.Unlock()
			evdev.Ioctl(d.fd, ioctlEndFFErase, unsafe.Pointer(&er[0]))
		}
	case evdev.EvFF:
		if ev.Code == evdev.FFGain || d.onRumble == nil {
			return
		}
		d.mu.Lock()
		e, ok := d.effects[int16(ev.Code)]
		d.mu.Unlock()
		if !ok {
			return
		}
		if ev.Value > 0 {
			d.onRumble(e[0], e[1])
		} else {
			d.onRumble(0, 0)
		}
	}
}
