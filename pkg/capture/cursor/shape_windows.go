//go:build windows

package cursor

import (
	"errors"
	"fmt"
	"runtime"
	"syscall"
	"unsafe"
)

var (
	gdi32  = syscall.NewLazyDLL("gdi32.dll")
	shcore = syscall.NewLazyDLL("shcore.dll")

	procGetIconInfo                  = user32.NewProc("GetIconInfo")
	procGetDC                        = user32.NewProc("GetDC")
	procReleaseDC                    = user32.NewProc("ReleaseDC")
	procMonitorFromPoint             = user32.NewProc("MonitorFromPoint")
	procSetThreadDpiAwarenessContext = user32.NewProc("SetThreadDpiAwarenessContext")
	procGetObjectW                   = gdi32.NewProc("GetObjectW")
	procGetDIBits                    = gdi32.NewProc("GetDIBits")
	procDeleteObject                 = gdi32.NewProc("DeleteObject")
	procGetDpiForMonitor             = shcore.NewProc("GetDpiForMonitor")
)

// iconInfo is ICONINFO.
type iconInfo struct {
	isIcon     int32
	hotX, hotY uint32
	mask       uintptr
	color      uintptr
}

// bitmapObj is BITMAP.
type bitmapObj struct {
	typ, width, height, widthBytes int32
	planes, bitsPixel              uint16
	bits                           uintptr
}

// bitmapInfo is BITMAPINFO with the two colors of a monochrome DIB.
type bitmapInfo struct {
	size                   uint32
	width, height          int32
	planes, bitCount       uint16
	compression, sizeImage uint32
	xPerMeter, yPerMeter   int32
	clrUsed, clrImportant  uint32
	colors                 [2]uint32
}

const (
	dibRGBColors          = 0
	monitorDefaultNearest = 2
	mdtEffectiveDPI       = 0
	// dpiPerMonitorAwareV2 is DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2.
	dpiPerMonitorAwareV2 = ^uintptr(3)
)

var errNoCursor = errors.New("cursor: no cursor shows")

// readShape reads the cursor's masks and color through GetIconInfo, which
// hands over two bitmaps that it deletes before it returns.
func readShape(s State) (Shape, error) {
	if s.Handle == 0 {
		return Shape{}, errNoCursor
	}
	var ii iconInfo
	if r, _, err := procGetIconInfo.Call(s.Handle, uintptr(unsafe.Pointer(&ii))); r == 0 {
		return Shape{}, fmt.Errorf("cursor: GetIconInfo: %w", err)
	}
	defer procDeleteObject.Call(ii.mask)
	if ii.color != 0 {
		defer procDeleteObject.Call(ii.color)
	}
	var bm bitmapObj
	if r, _, err := procGetObjectW.Call(ii.mask, unsafe.Sizeof(bm), uintptr(unsafe.Pointer(&bm))); r == 0 {
		return Shape{}, fmt.Errorf("cursor: GetObject: %w", err)
	}
	w, mh := int(bm.width), int(bm.height)
	h := mh
	if ii.color == 0 {
		h = mh / 2
	}
	if w <= 0 || h <= 0 || w > 4*MaxSide || h > 4*MaxSide {
		return Shape{}, fmt.Errorf("cursor: a %dx%d cursor", w, mh)
	}
	dc, _, _ := procGetDC.Call(0)
	if dc == 0 {
		return Shape{}, errors.New("cursor: GetDC failed")
	}
	defer procReleaseDC.Call(0, dc)
	mask, err := maskBits(dc, ii.mask, w, mh)
	if err != nil {
		return Shape{}, err
	}
	var px []byte
	if ii.color != 0 {
		bgra, err := colorBits(dc, ii.color, w, h)
		if err != nil {
			return Shape{}, err
		}
		px = fromColor(bgra, mask, w, h)
	} else {
		px = fromMono(mask, w, h)
	}
	return fit(Shape{W: w, H: h, HotX: min(int(ii.hotX), w-1), HotY: min(int(ii.hotY), h-1), DPI: dpiAt(s.X, s.Y), RGBA: px}), nil
}

// maskBits reads a w x h bitmap as a monochrome DIB, one byte a pixel.
func maskBits(dc, bmp uintptr, w, h int) ([]byte, error) {
	stride := (w + 31) / 32 * 4
	bits := make([]byte, stride*h)
	bi := bitmapInfo{width: int32(w), height: -int32(h), planes: 1, bitCount: 1}
	bi.size = uint32(unsafe.Offsetof(bi.colors))
	if n, _, err := procGetDIBits.Call(dc, bmp, 0, uintptr(h), uintptr(unsafe.Pointer(&bits[0])), uintptr(unsafe.Pointer(&bi)), dibRGBColors); int(n) != h {
		return nil, fmt.Errorf("cursor: GetDIBits of the mask: %w", err)
	}
	return unpackMask(bits, stride, w, h), nil
}

// colorBits reads a w x h bitmap as a 32-bit BGRA DIB, top down.
func colorBits(dc, bmp uintptr, w, h int) ([]byte, error) {
	bgra := make([]byte, w*h*4)
	bi := bitmapInfo{width: int32(w), height: -int32(h), planes: 1, bitCount: 32}
	bi.size = uint32(unsafe.Offsetof(bi.colors))
	if n, _, err := procGetDIBits.Call(dc, bmp, 0, uintptr(h), uintptr(unsafe.Pointer(&bgra[0])), uintptr(unsafe.Pointer(&bi)), dibRGBColors); int(n) != h {
		return nil, fmt.Errorf("cursor: GetDIBits of the color: %w", err)
	}
	return bgra, nil
}

// dpiAt is the effective DPI of the display at the physical point x, y.
// A DPI-unaware process sees 96 everywhere, so the thread asks as one
// aware of each display.
func dpiAt(x, y int) int {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if procSetThreadDpiAwarenessContext.Find() == nil && procGetDpiForMonitor.Find() == nil {
		if old, _, _ := procSetThreadDpiAwarenessContext.Call(dpiPerMonitorAwareV2); old != 0 {
			defer procSetThreadDpiAwarenessContext.Call(old)
		}
		mon, _, _ := procMonitorFromPoint.Call(uintptr(uint32(int32(x)))|uintptr(uint32(int32(y)))<<32, monitorDefaultNearest)
		var dx, dy uint32
		if r, _, _ := procGetDpiForMonitor.Call(mon, mdtEffectiveDPI, uintptr(unsafe.Pointer(&dx)), uintptr(unsafe.Pointer(&dy))); r == 0 && dx != 0 {
			return int(dx)
		}
	}
	return 96
}
