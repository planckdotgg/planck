//go:build darwin && !ios

package gpusurface

import (
	"testing"
	"time"
)

// TestGPUEndTime reads a finished command buffer's end time on Now's
// clock, and 0 for one not committed.
func TestGPUEndTime(t *testing.T) {
	win, _, _ := testWindow(t, 400, 300)
	var s *Surface
	var err error
	onMain(func() { s, err = New(Options{Window: win, Composite: true}) })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { onMain(s.Close) })
	pending := send(send(s.queue, "commandBuffer"), "retain")
	defer release(&pending)
	if got := gpuEndTime(pending); got != 0 {
		t.Fatalf("an uncommitted command buffer ended at %d", got)
	}
	start := Now()
	cb := send(send(s.queue, "commandBuffer"), "retain")
	defer release(&cb)
	send(cb, "commit")
	send(cb, "waitUntilCompleted")
	if got := gpuEndTime(cb); got < start || got > Now() {
		t.Fatalf("end %d ns after the start, want between it and now (%d)", got-start, Now()-start)
	}
}

// TestPresentedGPUReleases: with OnPresentedGPU set no drawable's slot
// holds its command buffer once its presented handler ran, and the handler costs
// no more than the registered call that reads the end time over
// OnPresented's: syscall6 cannot return a float.
func TestPresentedGPUReleases(t *testing.T) {
	frame := testIOSurface(t, 64, 48)
	allocs := func(gpu bool) (float64, *Surface) {
		win, _, _ := testWindow(t, 400, 300)
		var s *Surface
		var err error
		onMain(func() { s, err = New(Options{Window: win, Composite: true}) })
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { onMain(s.Close) })
		if gpu {
			s.OnPresentedGPU(func(tag, submitted, gpuDone, presented int64) {})
		} else {
			s.OnPresented(func(tag, submitted, presented int64) {})
		}
		present := func() {
			if err := s.PresentTagged(frame, 1); err != nil && err != errNoDrawable {
				t.Error(err)
			}
			time.Sleep(2 * time.Millisecond)
		}
		for range 5 {
			present()
		}
		return testing.AllocsPerRun(50, present), s
	}
	base, _ := allocs(false)
	withGPU, s := allocs(true)
	if withGPU > base+4 {
		t.Errorf("%.1f allocations a frame with OnPresentedGPU, %.1f with OnPresented", withGPU, base)
	}
	time.Sleep(100 * time.Millisecond)
	for i := range s.pending {
		if s.pending[i].cb.Load() != 0 {
			t.Errorf("slot %d still holds a command buffer", i)
		}
	}
}
