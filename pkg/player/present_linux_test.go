//go:build linux && !android && (amd64 || arm64)

package player

import (
	"testing"
	"unsafe"

	"github.com/egoist/mygo/gpusurface"

	"github.com/planckdotgg/planck/pkg/capture"
)

// TestDMABufLayouts: the surface reads a capture.DMABuf as its own
// gpusurface.DMABuf, so the two must be laid out alike.
func TestDMABufLayouts(t *testing.T) {
	var c capture.DMABuf
	var g gpusurface.DMABuf
	if unsafe.Sizeof(c) != unsafe.Sizeof(g) {
		t.Fatalf("sizes %d and %d", unsafe.Sizeof(c), unsafe.Sizeof(g))
	}
	for _, f := range []struct {
		name string
		a, b uintptr
	}{
		{"Fourcc", unsafe.Offsetof(c.Fourcc), unsafe.Offsetof(g.Fourcc)},
		{"Modifier", unsafe.Offsetof(c.Modifier), unsafe.Offsetof(g.Modifier)},
		{"Width", unsafe.Offsetof(c.Width), unsafe.Offsetof(g.Width)},
		{"Height", unsafe.Offsetof(c.Height), unsafe.Offsetof(g.Height)},
		{"Planes", unsafe.Offsetof(c.Planes), unsafe.Offsetof(g.Planes)},
		{"FDs", unsafe.Offsetof(c.FDs), unsafe.Offsetof(g.FDs)},
		{"Offsets", unsafe.Offsetof(c.Offsets), unsafe.Offsetof(g.Offsets)},
		{"Pitches", unsafe.Offsetof(c.Pitches), unsafe.Offsetof(g.Pitches)},
		{"CPU", unsafe.Offsetof(c.CPU), unsafe.Offsetof(g.CPU)},
		{"Memory", unsafe.Offsetof(c.Memory), unsafe.Offsetof(g.Memory)},
		{"Serial", unsafe.Offsetof(c.Serial), unsafe.Offsetof(g.Serial)},
	} {
		if f.a != f.b {
			t.Errorf("%s at %d and %d", f.name, f.a, f.b)
		}
	}
	if capture.FourccNV12 != gpusurface.FourccNV12 || capture.FourccP010 != gpusurface.FourccP010 {
		t.Error("fourccs differ")
	}
}
