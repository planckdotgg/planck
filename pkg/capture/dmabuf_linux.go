package capture

import "unsafe"

// DRM fourcc codes (drm_fourcc.h) of the layouts DMABuf carries.
const (
	FourccNV12     uint32 = 'N' | 'V'<<8 | '1'<<16 | '2'<<24
	FourccP010     uint32 = 'P' | '0'<<8 | '1'<<16 | '0'<<24
	FourccXRGB8888 uint32 = 'X' | 'R'<<8 | '2'<<16 | '4'<<24
	FourccARGB8888 uint32 = 'A' | 'R'<<8 | '2'<<16 | '4'<<24
	FourccXBGR8888 uint32 = 'X' | 'B'<<8 | '2'<<16 | '4'<<24
	FourccABGR8888 uint32 = 'A' | 'B'<<8 | '2'<<16 | '4'<<24
)

// ModifierLinear is DRM_FORMAT_MOD_LINEAR; ModifierInvalid says the
// producer gave none, and the importer picks the driver's implicit one.
const (
	ModifierLinear  uint64 = 0
	ModifierInvalid uint64 = 1<<56 - 1
)

// DMABuf is a picture on Linux: Frame.Surface, here and in package decode,
// points to one (see Handle and DMABufOf). Its producer owns it in a pool
// and keeps it, and its file descriptors, alive until the frame is
// released; consumers dup what they keep longer.
//
// FDs hold the GPU buffer of each plane, which may be one object for all
// planes. CPU, when set, holds the same planes in memory, row by row at
// Pitches: a producer fills it when its consumer cannot import DMA-BUFs
// (an X11 window, a compositor drawing in software, a headless test). A
// picture can have CPU planes and no FDs at all.
type DMABuf struct {
	Fourcc   uint32
	Modifier uint64
	// Width and Height are the visible picture; the planes may be larger.
	Width, Height int
	Planes        int
	FDs           [4]int32
	Offsets       [4]uint32
	Pitches       [4]uint32
	CPU           [4]unsafe.Pointer
	// Memory says FDs are memfds holding the CPU planes, linear, which a
	// Wayland compositor maps as wl_shm buffers.
	Memory bool
	// Serial tells pictures of one pool apart for a consumer that caches
	// what it made of them; a producer changes it whenever the buffers
	// behind the descriptor change.
	Serial uint64
}

// Handle is d as a Frame.Surface.
func (d *DMABuf) Handle() uintptr { return uintptr(unsafe.Pointer(d)) }

// DMABufOf is the DMABuf a Frame.Surface points to, nil for 0.
func DMABufOf(surface uintptr) *DMABuf {
	return (*DMABuf)(*(*unsafe.Pointer)(unsafe.Pointer(&surface)))
}

// HasFDs reports whether d's planes are in DMA-BUF objects. A descriptor
// without them leaves FDs zero: fd 0 is never one.
func (d *DMABuf) HasFDs() bool { return d.Planes > 0 && d.FDs[0] > 0 && !d.Memory }

// HasCPU reports whether d's planes are in memory.
func (d *DMABuf) HasCPU() bool { return d.Planes > 0 && d.CPU[0] != nil }
