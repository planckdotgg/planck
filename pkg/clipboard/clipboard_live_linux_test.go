package clipboard

import (
	"bytes"
	"context"
	"image/png"
	"os"
	"testing"
	"time"
)

// TestLiveBoard round-trips text and a PNG through the session's real
// clipboard tools and watches for a change another program makes. It
// replaces the clipboard's content.
func TestLiveBoard(t *testing.T) {
	if os.Getenv("PLANCK_LIVE_CLIPBOARD_TEST") != "1" {
		t.Skip("set PLANCK_LIVE_CLIPBOARD_TEST=1 to use the real clipboard")
	}
	b, err := Open()
	if err != nil {
		t.Skip(err)
	}
	defer b.Close()
	want := "héllo planck ☃"
	if err := b.Write(Item{Format: Text, Data: []byte(want)}); err != nil {
		t.Fatal(err)
	}
	it, ok, err := b.Read()
	if err != nil || !ok || it.Format != Text || string(it.Data) != want {
		t.Fatalf("read back %+v %v %v", it, ok, err)
	}
	png := testPNG(t)
	if err := b.Write(Item{Format: PNG, Data: png}); err != nil {
		if cb := b.(*cmdBoard); cb.xsel {
			t.Skip("xsel carries no images")
		}
		t.Fatal(err)
	}
	it, ok, _ = b.Read()
	if !ok || it.Format != PNG || string(it.Data) != string(png) {
		t.Fatalf("png read back %d bytes, ok %v, format %v", len(it.Data), ok, it.Format)
	}

	got := make(chan Item, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := b.Watch(ctx, func(it Item) { got <- it }); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * cmdPollInterval)
	if err := b.Write(Item{Format: Text, Data: []byte("changed by another app")}); err != nil {
		t.Fatal(err)
	}
	select {
	case it := <-got:
		if string(it.Data) != "changed by another app" {
			t.Errorf("watch reported %q", it.Data)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the change was not reported")
	}
}

func testPNG(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, testImage()); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
