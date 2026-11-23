//go:build darwin

package apple

import (
	"github.com/ebitengine/purego/objc"
)

type nsRect struct{ X, Y, W, H float64 }

type nsPoint struct{ X, Y float64 }

func sel(s string) objc.SEL { return objc.RegisterName(s) }

// RunWithAnimator runs work on a goroutine while the main thread drives a
// small borderless window that changes color on every timer tick, so screen
// capture sees motion. The app uses the accessory activation policy: no Dock
// icon, no focus change, mouse events pass through. It must be called on the
// main thread and returns after work does.
func RunWithAnimator(x, y, size float64, work func()) {
	_ = open("/System/Library/Frameworks/AppKit.framework/AppKit")
	app := objc.ID(objc.GetClass("NSApplication")).Send(sel("sharedApplication"))
	app.Send(sel("setActivationPolicy:"), 1)
	win := objc.ID(objc.GetClass("NSWindow")).Send(sel("alloc")).Send(
		sel("initWithContentRect:styleMask:backing:defer:"), nsRect{x, y, size, size}, uint(0), uint(2), false)
	win.Send(sel("setLevel:"), 25)
	win.Send(sel("setIgnoresMouseEvents:"), true)
	win.Send(sel("setOpaque:"), true)
	win.Send(sel("orderFrontRegardless"))
	color := objc.ID(objc.GetClass("NSColor"))
	i := 0
	tick := objc.NewBlock(func(_ objc.Block, _ objc.ID) {
		i++
		c := color.Send(sel("colorWithSRGBRed:green:blue:alpha:"),
			float64(i*37%256)/255, float64(i*91%256)/255, float64(i*13%256)/255, 1.0)
		win.Send(sel("setBackgroundColor:"), c)
	})
	objc.ID(objc.GetClass("NSTimer")).Send(sel("scheduledTimerWithTimeInterval:repeats:block:"), 1.0/240, true, tick)
	go func() {
		work()
		stop := objc.NewBlock(func(_ objc.Block, _ objc.ID) {
			app.Send(sel("stop:"), objc.ID(0))
			win.Send(sel("orderOut:"), objc.ID(0))
			// stop: takes effect after the next event; post one so run returns.
			ev := objc.ID(objc.GetClass("NSEvent")).Send(sel("otherEventWithType:location:modifierFlags:timestamp:windowNumber:context:subtype:data1:data2:"),
				uint(15), nsPoint{}, uint(0), 0.0, 0, objc.ID(0), int16(0), 0, 0)
			app.Send(sel("postEvent:atStart:"), ev, true)
		})
		objc.ID(objc.GetClass("NSOperationQueue")).Send(sel("mainQueue")).Send(sel("addOperationWithBlock:"), stop)
	}()
	app.Send(sel("run"))
}
