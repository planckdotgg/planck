package cursor

import (
	"bytes"
	"testing"
)

func rgbaAt(px []byte, w, x, y int) [4]byte {
	i := (y*w + x) * 4
	return [4]byte(px[i : i+4])
}

var (
	black = [4]byte{0, 0, 0, 255}
	white = [4]byte{255, 255, 255, 255}
	clear = [4]byte{}
)

// TestFromMonoIBeam: a monochrome cursor draws its black and white pixels
// as they are, and the pixels that invert the screen, as an I-beam's do,
// black with a white outline, so that it shows on dark and light alike.
func TestFromMonoIBeam(t *testing.T) {
	const w, h = 5, 4
	and := []byte{
		1, 1, 1, 1, 1,
		1, 1, 1, 1, 1,
		1, 1, 1, 1, 1,
		0, 0, 1, 1, 1,
	}
	xor := []byte{
		0, 0, 0, 0, 0,
		0, 0, 1, 0, 0,
		0, 0, 0, 0, 0,
		0, 1, 0, 0, 0,
	}
	px := fromMono(append(and, xor...), w, h)
	for _, c := range []struct {
		x, y int
		want [4]byte
	}{
		{2, 1, black}, {1, 0, white}, {3, 2, white}, {2, 2, white},
		{0, 0, clear}, {4, 1, clear}, {4, 3, clear},
		{0, 3, black}, {1, 3, white},
	} {
		if got := rgbaAt(px, w, c.x, c.y); got != c.want {
			t.Errorf("pixel %d,%d = %v, want %v", c.x, c.y, got, c.want)
		}
	}
}

// TestFromColor: a cursor with alpha keeps it and its colors, whatever the
// mask; one without alpha takes its transparency from the mask, and a
// color under a set mask inverts, drawn as a monochrome one's.
func TestFromColor(t *testing.T) {
	bgra := []byte{10, 20, 30, 128, 0, 0, 0, 0, 1, 2, 3, 255}
	px := fromColor(bgra, []byte{1, 1, 0}, 3, 1)
	if !bytes.Equal(px, []byte{30, 20, 10, 128, 0, 0, 0, 0, 3, 2, 1, 255}) {
		t.Errorf("with alpha: %v", px)
	}
	const w = 4
	bgra = []byte{
		10, 20, 30, 0,
		0, 0, 0, 0,
		255, 255, 255, 0,
		0, 0, 0, 0,
	}
	px = fromColor(bgra, []byte{0, 1, 1, 1}, w, 1)
	want := [][4]byte{{30, 20, 10, 255}, white, black, white}
	for x, c := range want {
		if got := rgbaAt(px, w, x, 0); got != c {
			t.Errorf("without alpha, pixel %d = %v, want %v", x, got, c)
		}
	}
}

func TestUnpackMask(t *testing.T) {
	got := unpackMask([]byte{0b1010_0000, 0, 0, 0, 0b0000_0001, 0b1000_0000, 0, 0}, 4, 9, 2)
	want := []byte{1, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 1}
	if !bytes.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// TestFit: a cursor over MaxSide shrinks to fit with its hotspot and DPI,
// keeping its size on the client; one within it is left alone.
func TestFit(t *testing.T) {
	big := Shape{W: 512, H: 384, HotX: 100, HotY: 383, DPI: 192, RGBA: make([]byte, 512*384*4)}
	big.RGBA[(383*512+511)*4] = 7
	s := fit(big)
	if s.W != 256 || s.H != 192 || s.HotX != 50 || s.HotY != 191 || s.DPI != 96 || len(s.RGBA) != 256*192*4 {
		t.Fatalf("fit %dx%d hot %d,%d dpi %d len %d", s.W, s.H, s.HotX, s.HotY, s.DPI, len(s.RGBA))
	}
	if s.RGBA[(191*256+255)*4] != 7 {
		t.Error("the corner pixel did not come along")
	}
	small := Shape{W: 32, H: 32, DPI: 96, RGBA: make([]byte, 32*32*4)}
	if got := fit(small); got.W != 32 || &got.RGBA[0] != &small.RGBA[0] {
		t.Error("a 32x32 cursor was scaled")
	}
}

func TestHash(t *testing.T) {
	a := Shape{W: 1, H: 1, DPI: 96, RGBA: []byte{1, 2, 3, 4}}
	b := a
	b.RGBA = []byte{1, 2, 3, 4}
	if a.Hash() != b.Hash() {
		t.Error("equal shapes hash apart")
	}
	for _, c := range []Shape{{W: 1, H: 1, DPI: 120, RGBA: a.RGBA}, {W: 1, H: 1, HotX: 1, DPI: 96, RGBA: a.RGBA}, {W: 1, H: 1, DPI: 96, RGBA: []byte{1, 2, 3, 5}}} {
		if c.Hash() == a.Hash() {
			t.Errorf("%+v hashes as %+v", c, a)
		}
	}
}
