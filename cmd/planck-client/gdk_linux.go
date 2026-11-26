package main

import (
	"math"
	"os"
	"strings"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/gpusurface"

	"github.com/planckdotgg/planck/pkg/defaults"
)

// GTK 3 and GDK through purego: MyGo's Linux backend loaded them already.
var gdk struct {
	once sync.Once
	err  error

	windowSetCursor, windowGetCursor, cursorNewFromName, cursorNewFromPixbuf, objectUnref uintptr
	pixbufNewFromData, pixbufCopy, displayGetDefault, displayDefaultSeat                  uintptr
	seatGrab, seatUngrab, monitorAtWindow, monitorGeometry, monitorScale, monitorRefresh  uintptr
	widgetScale                                                                           uintptr
}

func loadGDK() error {
	gdk.once.Do(func() {
		libs := map[string]uintptr{}
		for _, n := range []string{"libgtk-3.so.0", "libgdk-3.so.0", "libgobject-2.0.so.0", "libgdk_pixbuf-2.0.so.0"} {
			h, err := purego.Dlopen(n, purego.RTLD_NOW|purego.RTLD_GLOBAL)
			if err != nil {
				gdk.err = err
				return
			}
			libs[n] = h
		}
		for _, f := range []struct {
			lib  string
			p    *uintptr
			name string
		}{
			{"libgdk-3.so.0", &gdk.windowSetCursor, "gdk_window_set_cursor"},
			{"libgdk-3.so.0", &gdk.windowGetCursor, "gdk_window_get_cursor"},
			{"libgdk-3.so.0", &gdk.cursorNewFromName, "gdk_cursor_new_from_name"},
			{"libgdk-3.so.0", &gdk.cursorNewFromPixbuf, "gdk_cursor_new_from_pixbuf"},
			{"libgdk-3.so.0", &gdk.displayGetDefault, "gdk_display_get_default"},
			{"libgdk-3.so.0", &gdk.displayDefaultSeat, "gdk_display_get_default_seat"},
			{"libgdk-3.so.0", &gdk.seatGrab, "gdk_seat_grab"},
			{"libgdk-3.so.0", &gdk.seatUngrab, "gdk_seat_ungrab"},
			{"libgdk-3.so.0", &gdk.monitorAtWindow, "gdk_display_get_monitor_at_window"},
			{"libgdk-3.so.0", &gdk.monitorGeometry, "gdk_monitor_get_geometry"},
			{"libgdk-3.so.0", &gdk.monitorScale, "gdk_monitor_get_scale_factor"},
			{"libgdk-3.so.0", &gdk.monitorRefresh, "gdk_monitor_get_refresh_rate"},
			{"libgtk-3.so.0", &gdk.widgetScale, "gtk_widget_get_scale_factor"},
			{"libgobject-2.0.so.0", &gdk.objectUnref, "g_object_unref"},
			{"libgdk_pixbuf-2.0.so.0", &gdk.pixbufNewFromData, "gdk_pixbuf_new_from_data"},
			{"libgdk_pixbuf-2.0.so.0", &gdk.pixbufCopy, "gdk_pixbuf_copy"},
		} {
			sym, err := purego.Dlsym(libs[f.lib], f.name)
			if err != nil {
				gdk.err = err
				return
			}
			*f.p = sym
		}
	})
	return gdk.err
}

// ccall calls C; go:uintptrescapes keeps a pointer argument valid when the
// goroutine's stack moves before the call.
//
//go:uintptrescapes
func ccall(fn uintptr, args ...uintptr) uintptr {
	r, _, _ := purego.SyscallN(fn, args...)
	return r
}

func cstr(s string) []byte { return append([]byte(s), 0) }

// waylandSession reports whether GTK will open Wayland: a Wayland display
// to connect to and GDK_BACKEND not asking for another first.
func waylandSession() bool {
	if os.Getenv("WAYLAND_DISPLAY") == "" {
		return false
	}
	b := os.Getenv("GDK_BACKEND")
	return b == "" || strings.HasPrefix(b, "wayland") || b == "*"
}

// composeUnderUI: on Wayland the video's subsurface goes under the
// toplevel, which a transparent window shows it through. X11 blends no
// window over another, so there the window stays opaque and the frames
// are drawn in it (gpusurface picture mode).
var composeUnderUI = waylandSession()

// windowPixels is the window's content size in pixels: its DIPs at GTK's
// integer scale.
func windowPixels(w *mygo.Window) (int, int) {
	cw, ch := w.ContentSize()
	scale := 1
	if loadGDK() == nil && w.NativeHandle() != 0 {
		scale = max(int(int32(ccall(gdk.widgetScale, w.NativeHandle()))), 1)
	}
	return cw * scale, ch * scale
}

// windowDisplay is the monitor w is on, from GDK. Main thread.
func windowDisplay(w *mygo.Window) defaults.ClientDisplay {
	if loadGDK() != nil || w.NativeHandle() == 0 {
		return defaults.ClientDisplay{}
	}
	n, err := gpusurface.NativeOf(w.NativeHandle())
	if err != nil {
		return defaults.ClientDisplay{}
	}
	m := ccall(gdk.monitorAtWindow, ccall(gdk.displayGetDefault), n.GdkWindow)
	if m == 0 {
		return defaults.ClientDisplay{}
	}
	var geo struct{ x, y, w, h int32 }
	ccall(gdk.monitorGeometry, m, uintptr(unsafe.Pointer(&geo)))
	scale := max(int(int32(ccall(gdk.monitorScale, m))), 1)
	hz := int(math.Round(float64(int32(ccall(gdk.monitorRefresh, m))) / 1000))
	return defaults.ClientDisplay{WidthPx: int(geo.w) * scale, HeightPx: int(geo.h) * scale, Scale: float64(scale), MaxHz: hz, MinHz: hz}
}

func unsafePointer(b []byte) unsafe.Pointer { return unsafe.Pointer(&b[0]) }
