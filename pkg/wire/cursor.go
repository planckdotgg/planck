package wire

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"io"
)

// MaxCursorSide bounds a CursorShape's width and height, in pixels.
const MaxCursorSide = 256

// CursorCacheSize is how many shapes the host and the client each keep, the
// least recently used going first. Both see the same shapes in the same
// order, so a shape the host refers to by ID alone is still in the client's
// cache.
const CursorCacheSize = 32

// cursorEncZlib is RGBA compressed with zlib, the only encoding.
const cursorEncZlib = 1

const cursorHasImage = 1 << 0

// CursorShape is the host's cursor as the client draws it in place of its
// own over the picture. A shape with no image refers to one sent before,
// by ID.
type CursorShape struct {
	// ID names the shape: a hash of its size, hotspot, DPI and pixels.
	ID uint64
	// Width and Height are the image's size in pixels, up to
	// MaxCursorSide, and HotX and HotY its hotspot on it. All are zero in
	// a reference.
	Width, Height uint16
	HotX, HotY    uint16
	// DPI is the display's the host sized the cursor for; 96 is 100%.
	DPI uint16
	// RGBA is the image, not premultiplied, rows top down: Width * Height
	// * 4 bytes. It is nil in a reference.
	RGBA []byte
}

func (*CursorShape) Type() MsgType { return MsgCursorShape }

// Ref reports whether m refers to a shape sent before.
func (m *CursorShape) Ref() bool { return m.RGBA == nil }

func (m *CursorShape) bodyTooLarge() bool {
	return !m.Ref() && (m.Width > MaxCursorSide || m.Height > MaxCursorSide)
}

func (m *CursorShape) appendBody(b []byte) []byte {
	b = binary.BigEndian.AppendUint64(b, m.ID)
	if m.Ref() {
		return append(b, 0)
	}
	b = append(b, cursorHasImage)
	for _, v := range []uint16{m.Width, m.Height, m.HotX, m.HotY, m.DPI} {
		b = binary.BigEndian.AppendUint16(b, v)
	}
	var z bytes.Buffer
	zw, _ := zlib.NewWriterLevel(&z, zlib.BestCompression)
	zw.Write(m.RGBA)
	zw.Close()
	b = append(b, cursorEncZlib)
	b = binary.BigEndian.AppendUint32(b, uint32(z.Len()))
	return append(b, z.Bytes()...)
}

// decodeBody refuses an image of no size or over MaxCursorSide, a hotspot
// off it, an unknown encoding, and pixels that do not inflate to exactly
// the image's size.
func (m *CursorShape) decodeBody(d *decoder) {
	m.ID = d.u64()
	if d.u8()&cursorHasImage == 0 {
		return
	}
	m.Width, m.Height, m.HotX, m.HotY, m.DPI = d.u16(), d.u16(), d.u16(), d.u16(), d.u16()
	enc := d.u8()
	n := d.u32()
	if d.bad {
		return
	}
	if m.Width == 0 || m.Height == 0 || m.Width > MaxCursorSide || m.Height > MaxCursorSide ||
		m.HotX >= m.Width || m.HotY >= m.Height || m.DPI == 0 || enc != cursorEncZlib || uint64(n) > uint64(len(d.b)) {
		d.bad = true
		return
	}
	z := d.take(int(n))
	want := int(m.Width) * int(m.Height) * 4
	zr, err := zlib.NewReader(bytes.NewReader(z))
	if err != nil {
		d.bad = true
		return
	}
	px, err := io.ReadAll(io.LimitReader(zr, int64(want)+1))
	if err != nil || len(px) != want {
		d.bad = true
		return
	}
	m.RGBA = px
}
