package main

import (
	"testing"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"

	"github.com/planckdotgg/planck/pkg/wire"
)

// TestNSCursorSizeAndHotspot makes NSCursors of the host's shapes, never
// setting one: a 32 x 32 cursor of a 100% host is 32 points with its
// hotspot in points, the 48 x 48 one of a 150% host too, a hidden cursor
// is one clear point, and the cache keeps no more than its size.
func TestNSCursorSizeAndHotspot(t *testing.T) {
	if _, err := purego.Dlopen("/System/Library/Frameworks/AppKit.framework/AppKit", purego.RTLD_GLOBAL|purego.RTLD_NOW); err != nil {
		t.Fatal(err)
	}
	c := newHostCursor(nil).(*macCursor)
	defer c.close()
	px := make([]byte, 48*48*4)
	sizeOf := func(cur objc.ID) (nsSize, nsPoint) {
		return objc.Send[nsSize](cur.Send(objc.RegisterName("image")), objc.RegisterName("size")),
			objc.Send[nsPoint](cur, objc.RegisterName("hotSpot"))
	}
	for _, s := range []wire.CursorShape{
		{ID: 1, Width: 32, Height: 32, HotX: 4, HotY: 8, DPI: 96, RGBA: px[:32*32*4]},
		{ID: 2, Width: 48, Height: 48, HotX: 6, HotY: 12, DPI: 144, RGBA: px},
	} {
		cur := c.cursorOf(&s)
		if cur == 0 {
			t.Fatalf("shape %d: no NSCursor", s.ID)
		}
		if again := c.cursorOf(&s); again != cur {
			t.Fatalf("shape %d made twice", s.ID)
		}
		size, hot := sizeOf(cur)
		if size != (nsSize{32, 32}) || hot != (nsPoint{4, 8}) {
			t.Errorf("shape %d: %+v points, hotspot %+v", s.ID, size, hot)
		}
	}
	if size, _ := sizeOf(c.cursorOf(nil)); size != (nsSize{1, 1}) {
		t.Errorf("the clear cursor is %+v", size)
	}
	for i := range uint64(2 * wire.CursorCacheSize) {
		if c.cursorOf(&wire.CursorShape{ID: 100 + i, Width: 1, Height: 1, DPI: 96, RGBA: px[:4]}) == 0 {
			t.Fatal("no NSCursor")
		}
	}
	if len(c.cache.entries) != wire.CursorCacheSize {
		t.Fatalf("%d cursors cached", len(c.cache.entries))
	}
}
