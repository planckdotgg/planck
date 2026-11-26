// Package pipewire captures a Wayland desktop through the XDG desktop
// portal's ScreenCast interface: the portal asks the user which monitor
// to share (once, with a restore token after that) and hands over a
// PipeWire node, which a PipeWire stream (libpipewire-0.3, loaded with
// purego) consumes as DMA-BUFs or shared memory.
package pipewire

import (
	"encoding/binary"
	"errors"
)

// SPA POD types and keys (spa/utils/type.h, spa/param/), checked with a
// C program built against PipeWire 1.0.5's headers.
const (
	podID        = 3
	podInt       = 4
	podLong      = 5
	podRectangle = 10
	podFraction  = 11
	podObject    = 15
	podChoice    = 19

	objectFormat    = 0x40003
	objectBuffers   = 0x40004
	paramEnumFormat = 3
	paramFormat     = 4
	paramBuffers    = 5

	formatMediaType    = 1
	formatMediaSubtype = 2
	formatVideoFormat  = 0x20001
	formatModifier     = 0x20002
	formatSize         = 0x20003
	formatFramerate    = 0x20004
	mediaTypeVideo     = 2
	mediaSubtypeRaw    = 1

	choiceRange = 1
	choiceEnum  = 3

	propMandatory  = 8
	propDontFixate = 16

	buffersDataType = 6

	dataMemPtr = 1
	dataMemFd  = 2
	dataDmaBuf = 3
)

// SPA video formats (spa/param/video/raw.h).
const (
	videoRGBx = 7
	videoBGRx = 8
	videoRGBA = 11
	videoBGRA = 12
)

var le = binary.LittleEndian

// pod is a POD of typ around body, padded to 8 bytes.
func pod(typ uint32, body []byte) []byte {
	out := le.AppendUint32(nil, uint32(len(body)))
	out = le.AppendUint32(out, typ)
	out = append(out, body...)
	for len(out)%8 != 0 {
		out = append(out, 0)
	}
	return out
}

func u32s(v ...uint32) []byte {
	var b []byte
	for _, x := range v {
		b = le.AppendUint32(b, x)
	}
	return b
}

func idPod(v uint32) []byte      { return pod(podID, u32s(v)) }
func intPod(v int32) []byte      { return pod(podInt, u32s(uint32(v))) }
func longPod(v int64) []byte     { return pod(podLong, le.AppendUint64(nil, uint64(v))) }
func rectPod(w, h uint32) []byte { return pod(podRectangle, u32s(w, h)) }
func fracPod(n, d uint32) []byte { return pod(podFraction, u32s(n, d)) }

// choicePod is a choice of kind among values, the bodies of PODs of
// childType, each childSize bytes.
func choicePod(kind, childType, childSize uint32, values ...[]byte) []byte {
	body := u32s(kind, 0, childSize, childType)
	for _, v := range values {
		body = append(body, v...)
	}
	return pod(podChoice, body)
}

type prop struct {
	key, flags uint32
	value      []byte
}

func objectPod(typ, id uint32, props ...prop) []byte {
	body := u32s(typ, id)
	for _, p := range props {
		body = append(body, u32s(p.key, p.flags)...)
		body = append(body, p.value...)
	}
	return pod(podObject, body)
}

// formatChoices are the pixel formats asked for, the first the preferred.
var formatChoices = [][]byte{u32s(videoBGRx), u32s(videoBGRx), u32s(videoRGBx), u32s(videoBGRA), u32s(videoRGBA)}

// enumFormat is the EnumFormat a capture stream offers: 8-bit RGB at any
// size and rate, at w x h and fps by default. With modifiers it asks for
// DMA-BUFs of those layouts; without, for shared memory.
func enumFormat(w, h, fps uint32, modifiers []int64) []byte {
	props := []prop{
		{formatMediaType, 0, idPod(mediaTypeVideo)},
		{formatMediaSubtype, 0, idPod(mediaSubtypeRaw)},
		{formatVideoFormat, 0, choicePod(choiceEnum, podID, 4, formatChoices...)},
	}
	if len(modifiers) > 0 {
		vals := [][]byte{le.AppendUint64(nil, uint64(modifiers[0]))}
		for _, m := range modifiers {
			vals = append(vals, le.AppendUint64(nil, uint64(m)))
		}
		props = append(props, prop{formatModifier, propMandatory | propDontFixate, choicePod(choiceEnum, podLong, 8, vals...)})
	}
	props = append(props,
		prop{formatSize, 0, choicePod(choiceRange, podRectangle, 8, u32s(w, h), u32s(1, 1), u32s(8192, 8192))},
		prop{formatFramerate, 0, choicePod(choiceRange, podFraction, 8, u32s(fps, 1), u32s(0, 1), u32s(1000, 1))},
	)
	return objectPod(objectFormat, paramEnumFormat, props...)
}

// buffersParam asks for buffers of these data types (1 << SPA_DATA_*).
func buffersParam(dataTypes int32) []byte {
	return objectPod(objectBuffers, paramBuffers, prop{buffersDataType, 0, intPod(dataTypes)})
}

// videoFormat is what a negotiated Format says.
type videoFormat struct {
	format        uint32
	width, height uint32
	modifier      int64
	hasModifier   bool
}

var errPod = errors.New("pipewire: malformed format")

// parseFormat reads a Format object: the video format, size and
// modifier, the first value of any choice.
func parseFormat(b []byte) (videoFormat, error) {
	var f videoFormat
	if len(b) < 16 || le.Uint32(b[4:]) != podObject {
		return f, errPod
	}
	end := 8 + int(le.Uint32(b))
	if end > len(b) {
		return f, errPod
	}
	for off := 16; off+16 <= end; {
		key := le.Uint32(b[off:])
		size, typ := int(le.Uint32(b[off+8:])), le.Uint32(b[off+12:])
		body := off + 16
		if body+size > end {
			return f, errPod
		}
		v := b[body : body+size]
		if typ == podChoice && len(v) >= 16 {
			typ = le.Uint32(v[12:])
			v = v[16:]
		}
		switch {
		case key == formatVideoFormat && typ == podID && len(v) >= 4:
			f.format = le.Uint32(v)
		case key == formatSize && typ == podRectangle && len(v) >= 8:
			f.width, f.height = le.Uint32(v), le.Uint32(v[4:])
		case key == formatModifier && typ == podLong && len(v) >= 8:
			f.modifier, f.hasModifier = int64(le.Uint64(v)), true
		}
		off = body + (size+7)&^7
	}
	if f.format == 0 || f.width == 0 || f.height == 0 {
		return f, errPod
	}
	return f, nil
}
