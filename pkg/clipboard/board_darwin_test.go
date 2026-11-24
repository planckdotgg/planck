package clipboard

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"image/png"
	"strings"
	"testing"
	"time"

	"github.com/ebitengine/purego/objc"
)

// newTestBoard is a private named pasteboard, so tests never touch the
// user's clipboard.
func newTestBoard(t *testing.T) *pasteboard {
	loadAppKit()
	var b [6]byte
	rand.Read(b[:])
	p := &pasteboard{name: "gg.planck.test." + hex.EncodeToString(b[:])}
	t.Cleanup(func() {
		p.Close()
		withPool(func() { p.pb().Send(sel("releaseGlobally")) })
	})
	return p
}

func TestPasteboardTextAndImage(t *testing.T) {
	p := newTestBoard(t)
	if err := p.Write(Item{Format: Text, Data: []byte("héllo wörld")}); err != nil {
		t.Fatal(err)
	}
	it, ok, _ := p.Read()
	if !ok || it.Format != Text || string(it.Data) != "héllo wörld" {
		t.Fatalf("read back %v %q", ok, it.Data)
	}
	var img bytes.Buffer
	png.Encode(&img, testImage())
	if err := p.Write(Item{Format: PNG, Data: img.Bytes()}); err != nil {
		t.Fatal(err)
	}
	it, ok, _ = p.Read()
	if !ok || it.Format != PNG || !bytes.Equal(it.Data, img.Bytes()) {
		t.Fatalf("PNG read back: ok %v format %d, %d bytes", ok, it.Format, len(it.Data))
	}
}

// A TIFF-only image (as some apps copy) comes out as PNG.
func TestPasteboardTIFFBecomesPNG(t *testing.T) {
	p := newTestBoard(t)
	var img bytes.Buffer
	png.Encode(&img, testImage())
	withPool(func() {
		pb := p.pb()
		pb.Send(sel("clearContents"))
		rep := class("NSBitmapImageRep").Send(sel("imageRepWithData:"), nsData(img.Bytes()))
		pb.Send(sel("setData:forType:"), rep.Send(sel("TIFFRepresentation")), nsString(typeTIFF))
	})
	it, ok, _ := p.Read()
	if !ok || it.Format != PNG {
		t.Fatalf("TIFF read as ok %v format %d", ok, it.Format)
	}
	got, err := png.Decode(bytes.NewReader(it.Data))
	if err != nil || got.Bounds() != testImage().Bounds() {
		t.Fatalf("converted PNG: %v %v", err, got)
	}
}

// A password manager's copy carries ConcealedType: Read and Watch skip it.
func TestPasteboardSkipsConcealed(t *testing.T) {
	p := newTestBoard(t)
	changes := make(chan Item, 4)
	if err := p.Watch(context.Background(), func(it Item) { changes <- it }); err != nil {
		t.Fatal(err)
	}
	withPool(func() {
		pb := p.pb()
		pb.Send(sel("clearContents"))
		types := class("NSArray").Send(sel("arrayWithObjects:count:"), unsafeIDs(nsString(typeText), nsString("org.nspasteboard.ConcealedType")), 2)
		pb.Send(sel("declareTypes:owner:"), types, objc.ID(0))
		pb.Send(sel("setString:forType:"), nsString("hunter2"), nsString(typeText))
	})
	if _, ok, _ := p.Read(); ok {
		t.Fatal("Read returned concealed content")
	}
	p.Write(Item{Format: Text, Data: []byte("plain")})
	select {
	case it := <-changes:
		if strings.Contains(string(it.Data), "hunter2") || string(it.Data) != "plain" {
			t.Fatalf("watch delivered %q", it.Data)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watch missed the plain change")
	}
	select {
	case it := <-changes:
		t.Fatalf("watch delivered a second item %q", it.Data)
	case <-time.After(3 * pollInterval):
	}
}

func TestPasteboardRefusesOversize(t *testing.T) {
	p := newTestBoard(t)
	if err := p.Write(Item{Format: PNG, Data: make([]byte, MaxSize+1)}); err != ErrTooLarge {
		t.Fatalf("17 MiB write: %v", err)
	}
	withPool(func() {
		pb := p.pb()
		pb.Send(sel("clearContents"))
		pb.Send(sel("setData:forType:"), nsData(make([]byte, MaxSize+1)), nsString(typePNG))
	})
	it, ok, _ := p.Read()
	if !ok || !it.TooLarge() || it.Data != nil {
		t.Fatalf("oversize read: ok %v size %d data %d", ok, it.Size, len(it.Data))
	}
}
