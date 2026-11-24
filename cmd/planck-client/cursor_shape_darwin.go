package main

import (
	"bytes"
	"image"
	"image/png"
	"runtime"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
	"github.com/egoist/mygo"

	"github.com/planckdotgg/planck/pkg/wire"
)

type nsSize struct{ W, H float64 }

type nsPoint struct{ X, Y float64 }

var (
	loadCursorObjC     sync.Once
	msgSetSize         func(obj objc.ID, sel objc.SEL, size nsSize)
	msgInitWithHotSpot func(obj objc.ID, sel objc.SEL, img objc.ID, hot nsPoint) objc.ID
)

// macCursor sets NSCursors made of the host's shapes, sized in points as
// the host's DPI sizes them in pixels, so a 32 x 32 cursor at 100% is 32
// points on any display. MyGo sets its own cursor as the pointer enters
// its view and as the cursor its elements ask for changes, not on every
// move: the stream view asks for none over the picture, and set puts the
// host's back as the view builds after.
type macCursor struct {
	cache cursorCache[uint64, objc.ID]
	blank objc.ID
}

func newHostCursor(func() *mygo.Window) hostCursorSys {
	loadCursorObjC.Do(func() {
		purego.RegisterLibFunc(&msgSetSize, purego.RTLD_DEFAULT, "objc_msgSend")
		purego.RegisterLibFunc(&msgInitWithHotSpot, purego.RTLD_DEFAULT, "objc_msgSend")
	})
	return &macCursor{cache: cursorCache[uint64, objc.ID]{free: func(c objc.ID) { c.Send(objc.RegisterName("release")) }}}
}

func (c *macCursor) set(shape *wire.CursorShape) bool {
	want := c.cursorOf(shape)
	nsCursor := objc.ID(objc.GetClass("NSCursor"))
	if want == 0 || nsCursor.Send(objc.RegisterName("currentCursor")) == want {
		return false
	}
	want.Send(objc.RegisterName("set"))
	return true
}

// cursorOf is the NSCursor of shape, or a clear one for nil; 0 if AppKit
// makes none.
func (c *macCursor) cursorOf(shape *wire.CursorShape) objc.ID {
	if shape == nil {
		if c.blank == 0 {
			c.blank = newNSCursor(image.NewNRGBA(image.Rect(0, 0, 1, 1)), 1, 1, 0, 0)
		}
		return c.blank
	}
	cur, _ := c.cache.get(shape.ID, func() (objc.ID, bool) {
		k := cursorScale(shape, 96)
		cur := newNSCursor(rgbaImage(shape), float64(shape.Width)*k, float64(shape.Height)*k, float64(shape.HotX)*k, float64(shape.HotY)*k)
		return cur, cur != 0
	})
	return cur
}

func (c *macCursor) unset() {}

func (c *macCursor) close() {
	c.cache.close()
	if c.blank != 0 {
		c.blank.Send(objc.RegisterName("release"))
		c.blank = 0
	}
}

func rgbaImage(s *wire.CursorShape) *image.NRGBA {
	return &image.NRGBA{Pix: s.RGBA, Stride: int(s.Width) * 4, Rect: image.Rect(0, 0, int(s.Width), int(s.Height))}
}

// newNSCursor makes a retained NSCursor of img, w x h points, its hotspot
// x, y points from its top-left corner; 0 if AppKit takes none of it.
func newNSCursor(img *image.NRGBA, w, h, x, y float64) objc.ID {
	var b bytes.Buffer
	if img == nil || png.Encode(&b, img) != nil {
		return 0
	}
	sel := objc.RegisterName
	pngBytes := b.Bytes()
	data := objc.ID(objc.GetClass("NSData")).Send(sel("alloc")).
		Send(sel("initWithBytes:length:"), unsafe.Pointer(&pngBytes[0]), uintptr(len(pngBytes)))
	runtime.KeepAlive(pngBytes)
	defer data.Send(sel("release"))
	nsImg := objc.ID(objc.GetClass("NSImage")).Send(sel("alloc")).Send(sel("initWithData:"), data)
	if nsImg == 0 {
		return 0
	}
	defer nsImg.Send(sel("release"))
	msgSetSize(nsImg, sel("setSize:"), nsSize{w, h})
	return msgInitWithHotSpot(objc.ID(objc.GetClass("NSCursor")).Send(sel("alloc")), sel("initWithImage:hotSpot:"), nsImg, nsPoint{x, y})
}
