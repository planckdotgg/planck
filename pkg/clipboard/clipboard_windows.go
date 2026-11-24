//go:build windows

package clipboard

import (
	"context"
	"encoding/binary"
	"errors"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32                = windows.NewLazySystemDLL("user32.dll")
	kernel32              = windows.NewLazySystemDLL("kernel32.dll")
	procRegisterClassEx   = user32.NewProc("RegisterClassExW")
	procCreateWindowEx    = user32.NewProc("CreateWindowExW")
	procDefWindowProc     = user32.NewProc("DefWindowProcW")
	procDestroyWindow     = user32.NewProc("DestroyWindow")
	procGetMessage        = user32.NewProc("GetMessageW")
	procDispatchMessage   = user32.NewProc("DispatchMessageW")
	procPostMessage       = user32.NewProc("PostMessageW")
	procPostQuitMessage   = user32.NewProc("PostQuitMessage")
	procAddListener       = user32.NewProc("AddClipboardFormatListener")
	procRemoveListener    = user32.NewProc("RemoveClipboardFormatListener")
	procOpenClipboard     = user32.NewProc("OpenClipboard")
	procCloseClipboard    = user32.NewProc("CloseClipboard")
	procEmptyClipboard    = user32.NewProc("EmptyClipboard")
	procGetClipboardData  = user32.NewProc("GetClipboardData")
	procSetClipboardData  = user32.NewProc("SetClipboardData")
	procIsFormatAvailable = user32.NewProc("IsClipboardFormatAvailable")
	procRegisterFormat    = user32.NewProc("RegisterClipboardFormatW")
	procGetSequenceNumber = user32.NewProc("GetClipboardSequenceNumber")
	procGetModuleHandle   = kernel32.NewProc("GetModuleHandleW")
	procGlobalAlloc       = kernel32.NewProc("GlobalAlloc")
	procGlobalFree        = kernel32.NewProc("GlobalFree")
	procGlobalLock        = kernel32.NewProc("GlobalLock")
	procGlobalUnlock      = kernel32.NewProc("GlobalUnlock")
	procGlobalSize        = kernel32.NewProc("GlobalSize")
)

const (
	cfUnicodeText = 13
	cfDIB         = 8
	cfDIBV5       = 17

	wmDestroy         = 0x0002
	wmClose           = 0x0010
	wmClipboardUpdate = 0x031D
	wmApp             = 0x8000
	hwndMessage       = ^uintptr(2) // (HWND)-3
	gmemMoveable      = 0x0002

	// A DIB of maxPixels 32-bit pixels and its header.
	maxDIB = 4*maxPixels + 1024
)

// Registered formats. Password managers mark secrets with the first three
// (Windows clipboard history and cloud clipboard honour them too).
var (
	fmtExclude    = registerFormat("ExcludeClipboardContentFromMonitorProcessing")
	fmtNoHistory  = registerFormat("CanIncludeInClipboardHistory")
	fmtViewerSkip = registerFormat("Clipboard Viewer Ignore")
	fmtPNG        = registerFormat("PNG")
)

func registerFormat(name string) uintptr {
	p, _ := windows.UTF16PtrFromString(name)
	r, _, _ := procRegisterFormat.Call(uintptr(unsafe.Pointer(p)))
	return r
}

type wndClassEx struct {
	size       uint32
	style      uint32
	wndProc    uintptr
	clsExtra   int32
	wndExtra   int32
	instance   uintptr
	icon       uintptr
	cursor     uintptr
	background uintptr
	menuName   *uint16
	className  *uint16
	iconSm     uintptr
}

type msg struct {
	hwnd    uintptr
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	x, y    int32
	private uint32
}

var (
	classOnce sync.Once
	classErr  error
	className = windows.StringToUTF16Ptr("PlanckClipboard")
	windows_  sync.Map // hwnd -> *board
)

func wndProc(hwnd, message, wParam, lParam uintptr) uintptr {
	if v, ok := windows_.Load(hwnd); ok {
		b := v.(*board)
		switch message {
		case wmClipboardUpdate:
			b.changed()
			return 0
		case wmApp:
			b.runOps()
			return 0
		case wmClose:
			procDestroyWindow.Call(hwnd)
			return 0
		case wmDestroy:
			procPostQuitMessage.Call(0)
			return 0
		}
	}
	r, _, _ := procDefWindowProc.Call(hwnd, message, wParam, lParam)
	return r
}

func registerClass() {
	inst, _, _ := procGetModuleHandle.Call(0)
	wc := wndClassEx{wndProc: syscall.NewCallback(wndProc), instance: inst, className: className}
	wc.size = uint32(unsafe.Sizeof(wc))
	if r, _, err := procRegisterClassEx.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		classErr = err
	}
}

// board owns a message-only window on its own thread: the clipboard
// listener and every clipboard call run there.
type board struct {
	hwnd uintptr
	ops  chan func()
	done chan struct{}

	mu      sync.Mutex
	watch   func(Item)
	closed  bool
	lastSeq uintptr // set on the window thread only
}

func openBoard() (Board, error) {
	classOnce.Do(registerClass)
	if classErr != nil {
		return nil, classErr
	}
	b := &board{ops: make(chan func(), 16), done: make(chan struct{})}
	ready := make(chan error, 1)
	go b.loop(ready)
	if err := <-ready; err != nil {
		return nil, err
	}
	return b, nil
}

func (b *board) loop(ready chan<- error) {
	defer close(b.done)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	hwnd, _, err := procCreateWindowEx.Call(0, uintptr(unsafe.Pointer(className)), 0, 0, 0, 0, 0, 0, hwndMessage, 0, 0, 0)
	if hwnd == 0 {
		ready <- err
		return
	}
	b.hwnd = hwnd
	windows_.Store(hwnd, b)
	defer windows_.Delete(hwnd)
	if r, _, err := procAddListener.Call(hwnd); r == 0 {
		procDestroyWindow.Call(hwnd)
		ready <- err
		return
	}
	defer procRemoveListener.Call(hwnd)
	b.lastSeq, _, _ = procGetSequenceNumber.Call()
	ready <- nil
	var m msg
	for {
		r, _, _ := procGetMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			return
		}
		procDispatchMessage.Call(uintptr(unsafe.Pointer(&m)))
	}
}

var errClosed = errors.New("clipboard: closed")

// do runs fn on the window thread and waits for it.
func (b *board) do(fn func()) error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return errClosed
	}
	b.mu.Unlock()
	done := make(chan struct{})
	select {
	case b.ops <- func() { fn(); close(done) }:
	case <-b.done:
		return errClosed
	}
	procPostMessage.Call(b.hwnd, wmApp, 0, 0)
	select {
	case <-done:
		return nil
	case <-b.done:
		return errClosed
	}
}

func (b *board) runOps() {
	for {
		select {
		case fn := <-b.ops:
			fn()
		default:
			return
		}
	}
}

// changed runs on the window thread for WM_CLIPBOARDUPDATE. The sequence
// number skips repeats and Planck's own writes.
func (b *board) changed() {
	seq, _, _ := procGetSequenceNumber.Call()
	if seq == b.lastSeq {
		return
	}
	b.lastSeq = seq
	b.mu.Lock()
	watch := b.watch
	b.mu.Unlock()
	if watch == nil {
		return
	}
	if it, ok := b.read(); ok {
		watch(it)
	}
}

func (b *board) Watch(ctx context.Context, onChange func(Item)) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return errClosed
	}
	if b.watch != nil {
		return errWatching
	}
	b.watch = onChange
	go func() {
		select {
		case <-ctx.Done():
		case <-b.done:
		}
		b.mu.Lock()
		b.watch = nil
		b.mu.Unlock()
	}()
	return nil
}

func (b *board) Read() (it Item, ok bool, err error) {
	err = b.do(func() { it, ok = b.read() })
	return it, ok, err
}

// open opens the clipboard, retrying while another app holds it.
func (b *board) open() bool {
	for range 10 {
		if r, _, _ := procOpenClipboard.Call(b.hwnd); r != 0 {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

func available(format uintptr) bool {
	r, _, _ := procIsFormatAvailable.Call(format)
	return r != 0
}

// global copies up to limit bytes of a clipboard handle; size is the whole.
func global(h uintptr, limit int) (data []byte, size int) {
	n, _, _ := procGlobalSize.Call(h)
	size = int(n)
	if size > limit {
		return nil, size
	}
	p, _, _ := procGlobalLock.Call(h)
	if p == 0 {
		return nil, 0
	}
	defer procGlobalUnlock.Call(h)
	return append([]byte(nil), unsafe.Slice((*byte)(cPointer(p)), size)...), size
}

// read runs on the window thread.
func (b *board) read() (Item, bool) {
	if !b.open() {
		return Item{}, false
	}
	defer procCloseClipboard.Call()
	if available(fmtExclude) || available(fmtViewerSkip) {
		return Item{}, false
	}
	if available(fmtNoHistory) {
		if h, _, _ := procGetClipboardData.Call(fmtNoHistory); h != 0 {
			if v, _ := global(h, 64); len(v) >= 4 && binary.LittleEndian.Uint32(v) == 0 {
				return Item{}, false
			}
		}
	}
	data := func(format uintptr, limit int) ([]byte, int, bool) {
		h, _, _ := procGetClipboardData.Call(format)
		if h == 0 {
			return nil, 0, false
		}
		d, n := global(h, limit)
		return d, n, n > 0
	}
	if available(cfUnicodeText) {
		// UTF-16 takes at most twice as many bytes as the UTF-8 it maps to
		// for the BMP; a text past 4*MaxSize cannot fit.
		raw, n, ok := data(cfUnicodeText, 4*MaxSize)
		if !ok {
			return Item{}, false
		}
		if raw == nil {
			return Item{Format: Text, Size: n}, true
		}
		u := unsafe.Slice((*uint16)(unsafe.Pointer(&raw[0])), len(raw)/2)
		for i, c := range u {
			if c == 0 {
				u = u[:i]
				break
			}
		}
		text := []byte(string(utf16.Decode(u)))
		if len(text) > MaxSize {
			return Item{Format: Text, Size: len(text)}, true
		}
		return Item{Format: Text, Data: text, Size: len(text)}, true
	}
	if available(fmtPNG) {
		if d, n, ok := data(fmtPNG, MaxSize); ok {
			return Item{Format: PNG, Data: d, Size: n}, true
		}
	}
	for _, f := range []uintptr{cfDIBV5, cfDIB} {
		if !available(f) {
			continue
		}
		d, n, ok := data(f, maxDIB)
		if !ok || d == nil {
			return Item{Format: PNG, Size: n}, ok
		}
		png, err := dibToPNG(d)
		if err != nil {
			continue
		}
		if len(png) > MaxSize {
			return Item{Format: PNG, Size: len(png)}, true
		}
		return Item{Format: PNG, Data: png, Size: len(png)}, true
	}
	return Item{}, false
}

// cPointer turns an address the OS returned into a pointer.
func cPointer(p uintptr) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(&p)) }

func setGlobal(format uintptr, data []byte) bool {
	h, _, _ := procGlobalAlloc.Call(gmemMoveable, uintptr(max(len(data), 1)))
	if h == 0 {
		return false
	}
	p, _, _ := procGlobalLock.Call(h)
	if p == 0 {
		procGlobalFree.Call(h)
		return false
	}
	copy(unsafe.Slice((*byte)(cPointer(p)), len(data)), data)
	procGlobalUnlock.Call(h)
	if r, _, _ := procSetClipboardData.Call(format, h); r == 0 {
		procGlobalFree.Call(h)
		return false
	}
	return true
}

var errBusy = errors.New("clipboard: another app holds the clipboard")

// Write puts it on the clipboard: text as CF_UNICODETEXT, an image as PNG
// and CF_DIB for apps that do not read PNG.
func (b *board) Write(it Item) error {
	if len(it.Data) > MaxSize {
		return ErrTooLarge
	}
	var formats []uintptr
	var blobs [][]byte
	switch it.Format {
	case Text:
		u := utf16.Encode([]rune(string(it.Data)))
		blob := make([]byte, 2*len(u)+2)
		for i, c := range u {
			binary.LittleEndian.PutUint16(blob[2*i:], c)
		}
		formats, blobs = []uintptr{cfUnicodeText}, [][]byte{blob}
	case PNG:
		formats, blobs = []uintptr{fmtPNG}, [][]byte{it.Data}
		if dib, err := pngToDIB(it.Data); err == nil {
			formats, blobs = append(formats, cfDIB), append(blobs, dib)
		}
	default:
		return ErrFormat
	}
	var err error
	doErr := b.do(func() {
		if !b.open() {
			err = errBusy
			return
		}
		procEmptyClipboard.Call()
		for i, f := range formats {
			if !setGlobal(f, blobs[i]) {
				err = errors.New("clipboard: SetClipboardData failed")
			}
		}
		procCloseClipboard.Call()
		// The listener sees this write as one update, or none yet: either
		// way it is not another app's change.
		b.lastSeq, _, _ = procGetSequenceNumber.Call()
	})
	if doErr != nil {
		return doErr
	}
	return err
}

func (b *board) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	b.mu.Unlock()
	procPostMessage.Call(b.hwnd, wmClose, 0, 0)
	<-b.done
	return nil
}
