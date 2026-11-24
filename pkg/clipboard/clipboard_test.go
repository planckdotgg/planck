package clipboard

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"testing"
)

// Two devices share one clipboard item: the receiver applies it once, its
// own write coming back from the OS is not sent back, and the sender does
// not apply its own item if the peer echoes it.
func TestGuardStopsLoops(t *testing.T) {
	host, client := NewGuard(), NewGuard()
	if host.Origin() == client.Origin() {
		t.Fatal("two guards share an origin")
	}
	data := []byte("copied on the client")
	hash, send := client.Outgoing(data)
	if !send {
		t.Fatal("a fresh local change was not sent")
	}
	if !host.Incoming(client.Origin(), hash) {
		t.Fatal("the host did not apply the client's item")
	}
	// The host's write fires its own clipboard watcher.
	if _, send := host.Outgoing(data); send {
		t.Fatal("the host sent the client's item back")
	}
	// An echo that did get out is dropped by its origin...
	if client.Incoming(client.Origin(), hash) {
		t.Fatal("the client applied an item of its own origin")
	}
	// ...or by hash, if it was re-stamped.
	if client.Incoming(host.Origin(), hash) {
		t.Fatal("the client applied the item it just sent")
	}
	if host.Incoming(client.Origin(), hash) {
		t.Fatal("the host applied the same item twice")
	}
	// New content flows again.
	h2, send := host.Outgoing([]byte("copied on the host"))
	if !send || !client.Incoming(host.Origin(), h2) {
		t.Fatal("new host content did not reach the client")
	}
	if h2 != sha256.Sum256([]byte("copied on the host")) {
		t.Fatal("Outgoing's hash is not the SHA-256 of the data")
	}
}

// Copying A, then receiving B, then copying A again must send A again; and
// receiving B again after that must apply it.
func TestGuardForgetsOlderTransfers(t *testing.T) {
	host, client := NewGuard(), NewGuard()
	a, b := []byte("A"), []byte("B")
	ha, _ := client.Outgoing(a)
	host.Incoming(client.Origin(), ha)
	hb, _ := host.Outgoing(b)
	if !client.Incoming(host.Origin(), hb) {
		t.Fatal("B did not reach the client")
	}
	if _, send := client.Outgoing(a); !send {
		t.Fatal("copying A again on the client was not sent")
	}
	if !host.Incoming(client.Origin(), ha) {
		t.Fatal("the host did not apply A the second time")
	}
	if _, send := host.Outgoing(b); !send {
		t.Fatal("copying B again on the host was not sent")
	}
	if !client.Incoming(host.Origin(), hb) {
		t.Fatal("the client did not apply B the second time")
	}
}

func testImage() *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, 5, 3))
	for y := range 3 {
		for x := range 5 {
			img.SetNRGBA(x, y, color.NRGBA{uint8(40 * x), uint8(80 * y), 7, 255})
		}
	}
	return img
}

func TestPNGToDIBAndBack(t *testing.T) {
	var b bytes.Buffer
	png.Encode(&b, testImage())
	dib, err := pngToDIB(b.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	back, err := dibToPNG(dib)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(back))
	if err != nil {
		t.Fatal(err)
	}
	want := testImage()
	for y := range 3 {
		for x := range 5 {
			if got := color.NRGBAModel.Convert(img.At(x, y)); got != want.At(x, y) {
				t.Fatalf("pixel %d,%d = %v, want %v", x, y, got, want.At(x, y))
			}
		}
	}
}

// A 24-bit bottom-up DIB, as many Windows apps put on the clipboard.
func TestDIB24BottomUp(t *testing.T) {
	const w, h = 2, 2
	stride := 8
	dib := make([]byte, 40+stride*h)
	le := binary.LittleEndian
	le.PutUint32(dib, 40)
	le.PutUint32(dib[4:], w)
	le.PutUint32(dib[8:], h)
	le.PutUint16(dib[12:], 1)
	le.PutUint16(dib[14:], 24)
	// Bottom row first: blue, green; top row: red, white.
	copy(dib[40:], []byte{255, 0, 0, 0, 255, 0})
	copy(dib[40+stride:], []byte{0, 0, 255, 255, 255, 255})
	out, err := dibToPNG(dib)
	if err != nil {
		t.Fatal(err)
	}
	img, _ := png.Decode(bytes.NewReader(out))
	for _, c := range []struct {
		x, y int
		want color.NRGBA
	}{{0, 0, color.NRGBA{255, 0, 0, 255}}, {1, 0, color.NRGBA{255, 255, 255, 255}}, {0, 1, color.NRGBA{0, 0, 255, 255}}, {1, 1, color.NRGBA{0, 255, 0, 255}}} {
		if got := color.NRGBAModel.Convert(img.At(c.x, c.y)); got != c.want {
			t.Errorf("pixel %d,%d = %v, want %v", c.x, c.y, got, c.want)
		}
	}
}

func TestDIBRejectsTruncated(t *testing.T) {
	var b bytes.Buffer
	png.Encode(&b, testImage())
	dib, _ := pngToDIB(b.Bytes())
	if _, err := dibToPNG(dib[:len(dib)-1]); err == nil {
		t.Fatal("truncated bitmap converted")
	}
}
