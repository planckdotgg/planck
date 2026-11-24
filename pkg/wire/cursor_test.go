package wire

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"reflect"
	"testing"
)

// arrow is a 32x32 cursor as Windows draws its arrow: a white arrow with
// a black outline on transparent pixels.
func arrow() *CursorShape {
	const n = 32
	px := make([]byte, n*n*4)
	for y := range 21 {
		for x := 0; x <= y && x < 12; x++ {
			c := byte(255)
			if x == 0 || x == y || y == 20 {
				c = 0
			}
			i := (y*n + x) * 4
			px[i], px[i+1], px[i+2], px[i+3] = c, c, c, 255
		}
	}
	return &CursorShape{ID: 1, Width: n, Height: n, DPI: 96, RGBA: px}
}

// TestCursorShapeSize: a 32x32 cursor crosses the wire in well under a
// kilobyte, and a reference in a few bytes.
func TestCursorShapeSize(t *testing.T) {
	b, err := AppendMessage(nil, arrow())
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > 800 {
		t.Errorf("a 32x32 arrow takes %d bytes", len(b))
	}
	t.Logf("32x32 arrow: %d bytes", len(b))
	b, _ = AppendMessage(nil, &CursorShape{ID: 0xfedcba9876543210})
	if len(b) != 14 {
		t.Errorf("a reference takes %d bytes", len(b))
	}
	if m, err := DecodeMessage(b[4:]); err != nil || !reflect.DeepEqual(m, &CursorShape{ID: 0xfedcba9876543210}) || !m.(*CursorShape).Ref() {
		t.Errorf("a reference came back as %+v, %v", m, err)
	}
}

func shapeBody(id uint64, flags byte, w, h, hx, hy, dpi uint16, enc byte, z []byte) []byte {
	b := []byte{byte(MsgCursorShape)}
	b = binary.BigEndian.AppendUint64(b, id)
	b = append(b, flags)
	for _, v := range []uint16{w, h, hx, hy, dpi} {
		b = binary.BigEndian.AppendUint16(b, v)
	}
	b = append(b, enc)
	b = binary.BigEndian.AppendUint32(b, uint32(len(z)))
	return append(b, z...)
}

func deflate(px []byte) []byte {
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	zw.Write(px)
	zw.Close()
	return z.Bytes()
}

// TestCursorShapeRefused: a shape whose size, hotspot, DPI, encoding or
// pixels do not hold together is refused, not drawn.
func TestCursorShapeRefused(t *testing.T) {
	ok := deflate(make([]byte, 2*2*4))
	if m, err := DecodeMessage(shapeBody(7, cursorHasImage, 2, 2, 1, 1, 96, cursorEncZlib, ok)); err != nil ||
		!reflect.DeepEqual(m, &CursorShape{ID: 7, Width: 2, Height: 2, HotX: 1, HotY: 1, DPI: 96, RGBA: make([]byte, 16)}) {
		t.Fatalf("a valid shape: %+v, %v", m, err)
	}
	big := deflate(make([]byte, 257*1*4))
	for name, body := range map[string][]byte{
		"no width":            shapeBody(7, cursorHasImage, 0, 2, 0, 0, 96, cursorEncZlib, ok),
		"too wide":            shapeBody(7, cursorHasImage, 257, 1, 0, 0, 96, cursorEncZlib, big),
		"hotspot off it":      shapeBody(7, cursorHasImage, 2, 2, 2, 0, 96, cursorEncZlib, ok),
		"no DPI":              shapeBody(7, cursorHasImage, 2, 2, 0, 0, 0, cursorEncZlib, ok),
		"unknown encoding":    shapeBody(7, cursorHasImage, 2, 2, 0, 0, 96, 2, ok),
		"too few pixels":      shapeBody(7, cursorHasImage, 2, 2, 0, 0, 96, cursorEncZlib, deflate(make([]byte, 15))),
		"too many pixels":     shapeBody(7, cursorHasImage, 2, 2, 0, 0, 96, cursorEncZlib, deflate(make([]byte, 1<<20))),
		"not zlib":            shapeBody(7, cursorHasImage, 2, 2, 0, 0, 96, cursorEncZlib, []byte("not zlib at all")),
		"length past the end": shapeBody(7, cursorHasImage, 2, 2, 0, 0, 96, cursorEncZlib, ok)[:30],
		"trailing bytes":      append(shapeBody(7, cursorHasImage, 2, 2, 0, 0, 96, cursorEncZlib, ok), 0),
		"short reference":     {byte(MsgCursorShape), 0, 0, 0},
		"reference trailing":  {byte(MsgCursorShape), 0, 0, 0, 0, 0, 0, 0, 7, 0, 1},
	} {
		if _, err := DecodeMessage(body); err == nil {
			t.Errorf("%s: decoded", name)
		}
	}
	if _, err := AppendMessage(nil, &CursorShape{Width: 300, Height: 1, RGBA: make([]byte, 1200)}); !errors.Is(err, ErrTooLarge) {
		t.Errorf("a 300-wide shape: %v, want ErrTooLarge", err)
	}
	for _, f := range []uint32{FeatureLANDatagram, FeatureInputDedup, FeatureCapabilities, FeatureResize, FeaturePeerState, FeatureMic,
		FeatureClipboard, FeatureHostStats, FeatureCursorState, FeatureStreamSwitch, FeatureResizeDisplay} {
		if f&FeatureCursorShape != 0 {
			t.Errorf("FeatureCursorShape %#x shares a bit with %#x", FeatureCursorShape, f)
		}
	}
}
