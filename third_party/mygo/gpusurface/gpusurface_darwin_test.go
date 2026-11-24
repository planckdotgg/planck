//go:build darwin && !ios

package gpusurface

import (
	"os"
	"runtime"
	"testing"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

// AppKit takes windows and views on the main thread only: the tests run
// on another goroutine and hand their AppKit calls to onMain.
func init() { runtime.LockOSThread() }

var mainCalls = make(chan func())

// onMain runs fn on the main thread. fn reports failures with t.Error,
// not t.Fatal, which would end the main thread's loop.
func onMain(fn func()) {
	done := make(chan struct{})
	mainCalls <- func() { fn(); close(done) }
	<-done
}

var initWindow func(obj uintptr, sel objc.SEL, r cgRect, style, backing uintptr, deferred bool) uintptr

func TestMain(m *testing.M) {
	code := make(chan int, 1)
	go func() { code <- m.Run() }()
	for {
		select {
		case fn := <-mainCalls:
			fn()
		case c := <-code:
			os.Exit(c)
		}
	}
}

// testWindow makes a window of w x h points that is never shown, its
// content view holding one subview standing for MyGo's surface view.
func testWindow(t *testing.T, w, h float64) (win, content, ui uintptr) {
	if err := load(); err != nil {
		t.Fatal(err)
	}
	if initWindow == nil {
		if _, err := purego.Dlopen("/System/Library/Frameworks/AppKit.framework/AppKit", purego.RTLD_GLOBAL|purego.RTLD_NOW); err != nil {
			t.Fatal(err)
		}
		purego.RegisterFunc(&initWindow, lib.msgSend)
	}
	onMain(func() {
		send(class("NSApplication"), "sharedApplication")
		const styleTitled, backingBuffered = 1, 2
		win = initWindow(send(class("NSWindow"), "alloc"), objc.RegisterName("initWithContentRect:styleMask:backing:defer:"),
			cgRect{W: w, H: h}, styleTitled, backingBuffered, false)
		if win == 0 {
			return
		}
		send(win, "setReleasedWhenClosed:", false)
		content = send(win, "contentView")
		ui = lib.msgInitRect(send(class("NSView"), "alloc"), objc.RegisterName("initWithFrame:"), cgRect{W: w, H: h})
		send(ui, "setWantsLayer:", true)
		send(content, "addSubview:", ui)
	})
	if win == 0 {
		t.Fatal("cannot make a window")
	}
	t.Cleanup(func() {
		onMain(func() {
			send(win, "close")
			send(ui, "release")
			send(win, "release")
		})
	})
	return win, content, ui
}

func subviews(view uintptr) []uintptr {
	all := send(view, "subviews")
	n := int(send(all, "count"))
	out := make([]uintptr, n)
	for i := range out {
		out[i] = send(all, "objectAtIndex:", uintptr(i))
	}
	return out
}

// TestCompositeUnderContent: a composite surface's layer sits in a view
// at the back of the content view, under the views already there, that
// follows the window's size; Resize sets only the drawables, and Close
// takes the view away.
func TestCompositeUnderContent(t *testing.T) {
	win, content, ui := testWindow(t, 400, 300)
	var s *Surface
	var err error
	onMain(func() { s, err = New(Options{Window: win, Composite: true}) })
	if err != nil {
		t.Fatal(err)
	}
	onMain(func() {
		views := subviews(content)
		if len(views) != 2 || views[0] != s.host || views[1] != ui {
			t.Errorf("content subviews %#x, want [surface view %#x, UI view %#x]", views, s.host, ui)
		}
		if got := send(send(send(s.host, "layer"), "sublayers"), "firstObject"); got != s.layer {
			t.Errorf("the surface view's layer holds %#x, not the surface's layer %#x", got, s.layer)
		}
		lib.msgSetSize(content, objc.RegisterName("setFrameSize:"), cgSize{640, 480})
		if f := lib.msgRect(s.host, objc.RegisterName("frame")); f.W != 640 || f.H != 480 {
			t.Errorf("surface view frame %+v after the content view became 640x480", f)
		}
	})
	if err := s.Resize(1280, 960); err != nil {
		t.Fatal(err)
	}
	if w, h := s.Size(); w != 1280 || h != 960 {
		t.Fatalf("Size %dx%d after Resize(1280, 960)", w, h)
	}
	onMain(s.Close)
	onMain(func() {
		if views := subviews(content); len(views) != 1 || views[0] != ui {
			t.Errorf("content subviews after Close %#x, want only the UI view %#x", views, ui)
		}
	})
}
