//go:build windows

package pyrowave

import (
	"fmt"
	"unsafe"
)

// BudgetBytes is the record budget of the next frame.
func (e *Encoder) BudgetBytes() int { return 4 * int(e.budgetWords) }

// ReadTexture copies a decoded texture back to the CPU, for tests:
// tightly packed 32-bit pixels.
func (dd *D3D11Decoder) ReadTexture(tex uintptr) ([]byte, error) {
	var desc texture2DDesc
	call1(tex, texGetDesc, uintptr(unsafe.Pointer(&desc)))
	desc.Usage, desc.BindFlags, desc.CPUAccessFlags, desc.MiscFlags = usageStaging, 0, cpuAccessRead, 0
	var st uintptr
	if err := hr(call(dd.d.dev, devCreateTexture2D, uintptr(unsafe.Pointer(&desc)), 0, uintptr(unsafe.Pointer(&st)))); err != nil {
		return nil, fmt.Errorf("staging texture: %w", err)
	}
	defer release(&st)
	call(dd.d.ctx, ctxCopyResource, st, tex)
	dd.Wait()
	var m mapped
	if err := hr(call(dd.d.ctx, ctxMap, st, 0, mapRead, 0, uintptr(unsafe.Pointer(&m)))); err != nil {
		return nil, err
	}
	defer call(dd.d.ctx, ctxUnmap, st, 0)
	w, h := int(desc.Width), int(desc.Height)
	out := make([]byte, w*h*4)
	for y := 0; y < h; y++ {
		row := unsafe.Slice((*byte)(ptr(m.Data+uintptr(y)*uintptr(m.RowPitch))), w*4)
		copy(out[y*w*4:], row)
	}
	return out, nil
}

// Profile turns on GPU pass timing; GPUTimes returns the marks of the last
// frame, in ms since it began.
func (e *Encoder) Profile() error {
	p, err := e.d.newProfiler()
	e.prof = p
	return err
}

func (e *Encoder) GPUTimes() map[string]float64 { return e.prof.Last }

func (dd *D3D11Decoder) Profile() error {
	p, err := dd.d.newProfiler()
	dd.prof = p
	return err
}

func (dd *D3D11Decoder) GPUTimes() map[string]float64 { return dd.prof.Last }
