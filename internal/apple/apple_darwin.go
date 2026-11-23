//go:build darwin

// Package apple loads the CoreFoundation, CoreMedia, CoreVideo, and
// VideoToolbox entry points Planck needs through purego, with no cgo.
package apple

import (
	"fmt"
	"unsafe"

	"github.com/ebitengine/purego"
)

func open(path string) uintptr {
	h, err := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		panic(fmt.Sprintf("apple: dlopen %s: %v", path, err))
	}
	return h
}

var (
	System         = open("/usr/lib/libSystem.B.dylib")
	CoreFoundation = open("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation")
	CoreMedia      = open("/System/Library/Frameworks/CoreMedia.framework/CoreMedia")
	CoreVideo      = open("/System/Library/Frameworks/CoreVideo.framework/CoreVideo")
	VideoToolbox   = open("/System/Library/Frameworks/VideoToolbox.framework/VideoToolbox")
	IOSurface      = open("/System/Library/Frameworks/IOSurface.framework/IOSurface")
	CoreGraphics   = open("/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics")
)

// Sym returns a function or data address, panicking when it is missing.
func Sym(lib uintptr, name string) uintptr {
	p, err := purego.Dlsym(lib, name)
	if err != nil {
		panic(fmt.Sprintf("apple: dlsym %s: %v", name, err))
	}
	return p
}

// HasSym reports whether a symbol exists, for APIs newer than some macOS versions.
func HasSym(lib uintptr, name string) bool {
	_, err := purego.Dlsym(lib, name)
	return err == nil
}

// Const reads a global CFTypeRef constant such as kVTCompressionPropertyKey_RealTime.
func Const(lib uintptr, name string) uintptr {
	return **(**uintptr)(unsafe.Pointer(&[]uintptr{Sym(lib, name)}[0]))
}

func Register(fptr any, lib uintptr, name string) {
	purego.RegisterLibFunc(fptr, lib, name)
}

// CMTime mirrors CoreMedia's 24-byte time struct.
type CMTime struct {
	Value     int64
	Timescale int32
	Flags     uint32
	Epoch     int64
}

func Time(value int64, timescale int32) CMTime {
	return CMTime{Value: value, Timescale: timescale, Flags: 1}
}

var (
	CFRelease                 func(uintptr)
	CFRetain                  func(uintptr) uintptr
	CFStringCreateWithCString func(alloc uintptr, s string, enc uint32) uintptr
	CFNumberCreate            func(alloc uintptr, typ int, v unsafe.Pointer) uintptr
	CFNumberGetValue          func(n uintptr, typ int, v unsafe.Pointer) bool
	CFDictionaryCreateMutable func(alloc uintptr, cap int, keyCB, valueCB uintptr) uintptr
	CFDictionarySetValue      func(d, k, v uintptr)
	CFDictionaryGetValue      func(d, k uintptr) uintptr
	CFArrayGetCount           func(a uintptr) int
	CFArrayGetValueAtIndex    func(a uintptr, i int) uintptr
	CFGetRetainCount          func(uintptr) int

	MachAbsoluteTime func() uint64
	machTimebaseInfo func(info unsafe.Pointer) int32

	KCFBooleanTrue  uintptr
	KCFBooleanFalse uintptr
	keyCallbacks    uintptr
	valueCallbacks  uintptr
)

var numer, denom uint64

func init() {
	Register(&CFRelease, CoreFoundation, "CFRelease")
	Register(&CFRetain, CoreFoundation, "CFRetain")
	Register(&CFStringCreateWithCString, CoreFoundation, "CFStringCreateWithCString")
	Register(&CFNumberCreate, CoreFoundation, "CFNumberCreate")
	Register(&CFNumberGetValue, CoreFoundation, "CFNumberGetValue")
	Register(&CFDictionaryCreateMutable, CoreFoundation, "CFDictionaryCreateMutable")
	Register(&CFDictionarySetValue, CoreFoundation, "CFDictionarySetValue")
	Register(&CFDictionaryGetValue, CoreFoundation, "CFDictionaryGetValue")
	Register(&CFArrayGetCount, CoreFoundation, "CFArrayGetCount")
	Register(&CFArrayGetValueAtIndex, CoreFoundation, "CFArrayGetValueAtIndex")
	Register(&CFGetRetainCount, CoreFoundation, "CFGetRetainCount")
	Register(&MachAbsoluteTime, System, "mach_absolute_time")
	Register(&machTimebaseInfo, System, "mach_timebase_info")
	KCFBooleanTrue = Const(CoreFoundation, "kCFBooleanTrue")
	KCFBooleanFalse = Const(CoreFoundation, "kCFBooleanFalse")
	keyCallbacks = Sym(CoreFoundation, "kCFTypeDictionaryKeyCallBacks")
	valueCallbacks = Sym(CoreFoundation, "kCFTypeDictionaryValueCallBacks")
	var tb [2]uint32
	machTimebaseInfo(unsafe.Pointer(&tb))
	numer, denom = uint64(tb[0]), uint64(tb[1])
}

// Nanos converts mach_absolute_time ticks to nanoseconds.
func Nanos(ticks uint64) int64 { return int64(ticks * numer / denom) }

// Now is the mach clock in nanoseconds.
func Now() int64 { return Nanos(MachAbsoluteTime()) }

const (
	numberSInt32  = 3
	numberSInt64  = 4
	numberFloat64 = 13
	utf8          = 0x08000100
)

func String(s string) uintptr { return CFStringCreateWithCString(0, s, utf8) }

func Int(v int64) uintptr { return CFNumberCreate(0, numberSInt64, unsafe.Pointer(&v)) }

func Float(v float64) uintptr { return CFNumberCreate(0, numberFloat64, unsafe.Pointer(&v)) }

func Bool(v bool) uintptr {
	if v {
		return KCFBooleanTrue
	}
	return KCFBooleanFalse
}

// NumberInt64 reads a CFNumber as int64.
func NumberInt64(n uintptr) (int64, bool) {
	var v int64
	ok := n != 0 && CFNumberGetValue(n, numberSInt64, unsafe.Pointer(&v))
	return v, ok
}

// Dict builds a CFMutableDictionary from alternating keys and values. Values
// passed here are retained by the dictionary; the caller keeps its own refs.
func Dict(kv ...uintptr) uintptr {
	d := CFDictionaryCreateMutable(0, 0, keyCallbacks, valueCallbacks)
	for i := 0; i+1 < len(kv); i += 2 {
		CFDictionarySetValue(d, kv[i], kv[i+1])
	}
	return d
}
