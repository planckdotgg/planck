package clipboard

import (
	"bytes"
	"context"
	"encoding/binary"
	"image/png"
	"os"
	"testing"
	"time"
	"unicode/utf16"
)

// These tests use the real Windows clipboard, so they run only with
// PLANCK_LIVE_CLIPBOARD_TEST=1, and put back the text that was on it.
func openLive(t *testing.T) *board {
	if os.Getenv("PLANCK_LIVE_CLIPBOARD_TEST") != "1" {
		t.Skip("set PLANCK_LIVE_CLIPBOARD_TEST=1 to use the Windows clipboard")
	}
	bd, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	b := bd.(*board)
	saved, ok, _ := b.Read()
	t.Cleanup(func() {
		if ok && saved.Data != nil {
			b.Write(saved)
		}
		b.Close()
	})
	return b
}

func utf16z(s string) []byte {
	u := utf16.Encode([]rune(s))
	out := make([]byte, 2*len(u)+2)
	for i, c := range u {
		binary.LittleEndian.PutUint16(out[2*i:], c)
	}
	return out
}

// rawSet puts formats on the clipboard as another app would.
func rawSet(t *testing.T, b *board, formats map[uintptr][]byte) {
	b.do(func() {
		if !b.open() {
			t.Error("clipboard busy")
			return
		}
		procEmptyClipboard.Call()
		for f, d := range formats {
			setGlobal(f, d)
		}
		procCloseClipboard.Call()
	})
}

func TestLiveWindowsTextAndImage(t *testing.T) {
	b := openLive(t)
	if err := b.Write(Item{Format: Text, Data: []byte("héllo wörld ✓")}); err != nil {
		t.Fatal(err)
	}
	it, ok, err := b.Read()
	if err != nil || !ok || it.Format != Text || string(it.Data) != "héllo wörld ✓" {
		t.Fatalf("text read back %v %v %q", err, ok, it.Data)
	}
	var img bytes.Buffer
	png.Encode(&img, testImage())
	if err := b.Write(Item{Format: PNG, Data: img.Bytes()}); err != nil {
		t.Fatal(err)
	}
	it, ok, _ = b.Read()
	if !ok || it.Format != PNG || !bytes.Equal(it.Data, img.Bytes()) {
		t.Fatalf("PNG read back: ok %v format %d, %d bytes", ok, it.Format, len(it.Data))
	}
	var dib bool
	b.do(func() { b.open(); dib = available(cfDIB); procCloseClipboard.Call() })
	if !dib {
		t.Fatal("an image write left no CF_DIB for apps that do not read PNG")
	}
	// A bitmap-only copy, as from Paint, comes out as PNG.
	d, _ := pngToDIB(img.Bytes())
	other, _ := Open()
	defer other.Close()
	rawSet(t, other.(*board), map[uintptr][]byte{cfDIB: d})
	it, ok, _ = b.Read()
	if !ok || it.Format != PNG {
		t.Fatalf("CF_DIB read as ok %v format %d", ok, it.Format)
	}
	if got, err := png.Decode(bytes.NewReader(it.Data)); err != nil || got.Bounds() != testImage().Bounds() {
		t.Fatalf("CF_DIB to PNG: %v", err)
	}
}

// Watch reports other apps' changes, not Planck's own writes, and skips
// what a password manager marks.
func TestLiveWindowsWatch(t *testing.T) {
	b := openLive(t)
	other, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	changes := make(chan Item, 8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := b.Watch(ctx, func(it Item) { changes <- it }); err != nil {
		t.Fatal(err)
	}
	expectNone := func(what string) {
		select {
		case it := <-changes:
			t.Fatalf("%s: watch delivered %d bytes of format %d", what, len(it.Data), it.Format)
		case <-time.After(400 * time.Millisecond):
		}
	}
	b.Write(Item{Format: Text, Data: []byte("written by planck")})
	expectNone("own write")
	rawSet(t, other.(*board), map[uintptr][]byte{cfUnicodeText: utf16z("hunter2"), fmtExclude: {0}})
	expectNone("ExcludeClipboardContentFromMonitorProcessing")
	if _, ok, _ := b.Read(); ok {
		t.Fatal("Read returned excluded content")
	}
	rawSet(t, other.(*board), map[uintptr][]byte{cfUnicodeText: utf16z("hunter3"), fmtNoHistory: {0, 0, 0, 0}})
	expectNone("CanIncludeInClipboardHistory=0")
	other.Write(Item{Format: Text, Data: []byte("copied elsewhere")})
	select {
	case it := <-changes:
		if string(it.Data) != "copied elsewhere" {
			t.Fatalf("watch delivered %q", it.Data)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watch missed another app's change")
	}
	if err := b.Write(Item{Format: PNG, Data: make([]byte, MaxSize+1)}); err != ErrTooLarge {
		t.Fatalf("17 MiB write: %v", err)
	}
}
