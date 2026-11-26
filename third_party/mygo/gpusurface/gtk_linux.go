//go:build linux && !android && (amd64 || arm64)

package gpusurface

import (
	"errors"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

// MyGo's Linux backend is GTK 3, loaded with purego; the surface opens the
// same libraries (already loaded) for the handles under a GtkWindow*: its
// GdkWindow, the wl_display and wl_surface on Wayland, the XID on X11, and
// the GtkGLArea or GtkDrawingArea MyGo draws the window's content in.
var gtk struct {
	once sync.Once
	err  error

	widgetGetWindow, windowGetDisplay, widgetTranslate, widgetWidth, widgetHeight uintptr
	widgetScale, containerChildren, listFree, typeName, checkInstanceIsA          uintptr
	waylandDisplayType, waylandWLDisplay, waylandWLSurface, waylandSeat           uintptr
	displayGetDefaultSeat, x11Window, x11Display                                  uintptr
	widgetIsRealized                                                              uintptr
}

func loadGTK() error {
	gtk.once.Do(func() {
		g, err := purego.Dlopen("libgtk-3.so.0", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			gtk.err = err
			return
		}
		d, err := purego.Dlopen("libgdk-3.so.0", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			gtk.err = err
			return
		}
		o, err := purego.Dlopen("libgobject-2.0.so.0", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			gtk.err = err
			return
		}
		gl, err := purego.Dlopen("libglib-2.0.so.0", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			gtk.err = err
			return
		}
		for _, f := range []struct {
			lib  uintptr
			p    *uintptr
			name string
			opt  bool
		}{
			{g, &gtk.widgetGetWindow, "gtk_widget_get_window", false},
			{g, &gtk.widgetTranslate, "gtk_widget_translate_coordinates", false},
			{g, &gtk.widgetWidth, "gtk_widget_get_allocated_width", false},
			{g, &gtk.widgetHeight, "gtk_widget_get_allocated_height", false},
			{g, &gtk.widgetScale, "gtk_widget_get_scale_factor", false},
			{g, &gtk.widgetIsRealized, "gtk_widget_get_realized", false},
			{g, &gtk.containerChildren, "gtk_container_get_children", false},
			{d, &gtk.windowGetDisplay, "gdk_window_get_display", false},
			{d, &gtk.displayGetDefaultSeat, "gdk_display_get_default_seat", false},
			{d, &gtk.waylandDisplayType, "gdk_wayland_display_get_type", true},
			{d, &gtk.waylandWLDisplay, "gdk_wayland_display_get_wl_display", true},
			{d, &gtk.waylandWLSurface, "gdk_wayland_window_get_wl_surface", true},
			{d, &gtk.waylandSeat, "gdk_wayland_seat_get_wl_seat", true},
			{d, &gtk.x11Window, "gdk_x11_window_get_xid", true},
			{d, &gtk.x11Display, "gdk_x11_display_get_xdisplay", true},
			{o, &gtk.typeName, "g_type_name_from_instance", false},
			{o, &gtk.checkInstanceIsA, "g_type_check_instance_is_a", false},
			{gl, &gtk.listFree, "g_list_free", false},
		} {
			sym, err := purego.Dlsym(f.lib, f.name)
			if err != nil && !f.opt {
				gtk.err = err
				return
			}
			*f.p = sym
		}
	})
	return gtk.err
}

// call calls C. go:uintptrescapes keeps an argument made of a pointer
// valid if the goroutine's stack moves before the call.
//
//go:uintptrescapes
func call(fn uintptr, args ...uintptr) uintptr {
	r, _, _ := purego.SyscallN(fn, args...)
	return r
}

// Native is what the surface found under a MyGo window. Area's position
// and size are in the window's DIPs, relative to the toplevel GdkWindow,
// which on Wayland is the toplevel wl_surface's origin, shadow included.
type Native struct {
	// Wayland is set when GDK runs on Wayland; otherwise it runs on X11.
	Wayland bool
	// WLDisplay, WLSurface and WLSeat are GTK's wl_display, the
	// toplevel's wl_surface and the default seat's wl_seat, on Wayland.
	WLDisplay, WLSurface, WLSeat uintptr
	// XDisplay and XID are GTK's Display* and the toplevel's window, on
	// X11.
	XDisplay, XID uintptr
	// GdkWindow is the toplevel's; Area is the GtkWidget MyGo draws the
	// content in.
	GdkWindow, Area uintptr
	AreaX, AreaY    int
	AreaW, AreaH    int
	Scale           int
}

// NativeOf finds the handles under window, a GtkWindow* as
// mygo.Window.NativeHandle returns it. Call it on the main thread, once
// the window is realized.
func NativeOf(window uintptr) (Native, error) {
	if err := loadGTK(); err != nil {
		return Native{}, err
	}
	if window == 0 || call(gtk.widgetIsRealized, window)&1 == 0 {
		return Native{}, errors.New("gpusurface: the window is not realized")
	}
	n := Native{GdkWindow: call(gtk.widgetGetWindow, window)}
	if n.GdkWindow == 0 {
		return Native{}, errors.New("gpusurface: the window has no GdkWindow")
	}
	display := call(gtk.windowGetDisplay, n.GdkWindow)
	if gtk.waylandDisplayType != 0 && call(gtk.checkInstanceIsA, display, call(gtk.waylandDisplayType))&1 != 0 {
		n.Wayland = true
		n.WLDisplay = call(gtk.waylandWLDisplay, display)
		n.WLSurface = call(gtk.waylandWLSurface, n.GdkWindow)
		if seat := call(gtk.displayGetDefaultSeat, display); seat != 0 && gtk.waylandSeat != 0 {
			n.WLSeat = call(gtk.waylandSeat, seat)
		}
		if n.WLDisplay == 0 || n.WLSurface == 0 {
			return Native{}, errors.New("gpusurface: GTK gave no Wayland surface")
		}
	} else if gtk.x11Window != 0 {
		n.XID = call(gtk.x11Window, n.GdkWindow)
		n.XDisplay = call(gtk.x11Display, display)
	}
	n.Area = findArea(window)
	if n.Area == 0 {
		n.Area = window
	}
	n.layout(window)
	return n, nil
}

// layout reads the area's place in the toplevel. Main thread.
func (n *Native) layout(window uintptr) {
	var x, y int32
	if n.Area != window {
		call(gtk.widgetTranslate, n.Area, window, 0, 0, uintptr(unsafe.Pointer(&x)), uintptr(unsafe.Pointer(&y)))
	}
	n.AreaX, n.AreaY = int(x), int(y)
	n.AreaW, n.AreaH = int(int32(call(gtk.widgetWidth, n.Area))), int(int32(call(gtk.widgetHeight, n.Area)))
	n.Scale = max(int(int32(call(gtk.widgetScale, n.Area))), 1)
}

// findArea is the first GtkGLArea or GtkDrawingArea under w, depth first.
func findArea(w uintptr) uintptr {
	switch typeName(w) {
	case "GtkGLArea", "GtkDrawingArea":
		return w
	}
	if typeName(w) == "" || !isContainer(w) {
		return 0
	}
	list := call(gtk.containerChildren, w)
	defer call(gtk.listFree, list)
	for l := list; l != 0; l = *(*uintptr)(ptr(l + 8)) { // GList: data 0, next 8
		if a := findArea(*(*uintptr)(ptr(l))); a != 0 {
			return a
		}
	}
	return 0
}

var containerType = struct {
	once sync.Once
	t    uintptr
}{}

func isContainer(w uintptr) bool {
	containerType.once.Do(func() {
		if g, err := purego.Dlopen("libgtk-3.so.0", purego.RTLD_NOW|purego.RTLD_GLOBAL); err == nil {
			if sym, err := purego.Dlsym(g, "gtk_container_get_type"); err == nil {
				containerType.t = call(sym)
			}
		}
	})
	return containerType.t != 0 && call(gtk.checkInstanceIsA, w, containerType.t)&1 != 0
}

func typeName(w uintptr) string {
	p := call(gtk.typeName, w)
	if p == 0 {
		return ""
	}
	var b []byte
	for ; *(*byte)(ptr(p)) != 0; p++ {
		b = append(b, *(*byte)(ptr(p)))
	}
	return string(b)
}

func ptr(a uintptr) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(&a)) }
