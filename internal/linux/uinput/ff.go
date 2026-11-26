package uinput

import (
	"encoding/binary"

	"github.com/planckdotgg/planck/internal/linux/evdev"
)

const ffTypeRumble = evdev.FFRumble

func putRequest(b []byte, id uint32) { binary.NativeEndian.PutUint32(b, id) }

func putRetval(b []byte, v int32) { binary.NativeEndian.PutUint32(b[4:], uint32(v)) }

func eraseID(b []byte) uint32 { return binary.NativeEndian.Uint32(b[8:]) }

// parseUpload reads a rumble effect out of struct uinput_ff_upload; ok is
// false for any other effect type.
func parseUpload(b []byte) (id int16, strong, weak uint16, ok bool) {
	const effect = 8
	if binary.NativeEndian.Uint16(b[effect:]) != ffTypeRumble {
		return 0, 0, 0, false
	}
	id = int16(binary.NativeEndian.Uint16(b[effect+2:]))
	strong = binary.NativeEndian.Uint16(b[effect+evdev.FFRumbleOffset:])
	weak = binary.NativeEndian.Uint16(b[effect+evdev.FFRumbleOffset+2:])
	return id, strong, weak, true
}

// A force-feedback upload is struct uinput_ff_upload: request_id, retval,
// then the new and the old struct ff_effect.
const (
	ffUploadSize = 8 + 2*evdev.FFEffectSize
	ffEraseSize  = 12
)

var (
	ioctlBeginFFUpload = evdev.IOWR(uiType, 200, ffUploadSize)
	ioctlEndFFUpload   = evdev.IOW(uiType, 201, ffUploadSize)
	ioctlBeginFFErase  = evdev.IOWR(uiType, 202, ffEraseSize)
	ioctlEndFFErase    = evdev.IOW(uiType, 203, ffEraseSize)
)
