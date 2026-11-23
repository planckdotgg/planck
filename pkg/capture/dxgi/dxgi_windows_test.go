//go:build windows

package dxgi

import (
	"errors"
	"runtime"
	"testing"
	"time"

	"github.com/watzon/planck/internal/win"
	"github.com/watzon/planck/pkg/capture"
)

func TestAdaptersAndDisplays(t *testing.T) {
	adapters, err := Adapters()
	if err != nil {
		t.Fatal(err)
	}
	if len(adapters) == 0 {
		t.Fatal("no hardware adapter")
	}
	for _, a := range adapters {
		t.Logf("adapter %s vendor=%s luid=0x%x vram=%d MiB", a.Name, a.Vendor, uint64(a.LUID), a.VRAMBytes>>20)
	}
	displays, err := Displays()
	if err != nil {
		t.Fatal(err)
	}
	primary := 0
	for _, d := range displays {
		t.Logf("display %s on %s: %dx%d @ %.0f Hz hdr=%v primary=%v", d.ID, d.Adapter.Name, d.Width, d.Height, d.Refresh, d.HDR, d.Primary)
		if d.Primary {
			primary++
		}
	}
	if len(displays) > 0 && primary != 1 {
		t.Fatalf("want one primary display, got %d", primary)
	}
	dev, err := OpenDevice(adapters[0].LUID)
	if err != nil {
		t.Fatal(err)
	}
	dev.Close()
	if _, err := OpenDevice(1); err == nil {
		t.Fatal("OpenDevice of an unknown LUID succeeded")
	}
}

// animate shows a small square in a corner of the primary display so the
// compositor produces frames. It never takes focus.
func animate(t *testing.T, d capture.Display) {
	stop, done := make(chan struct{}), make(chan struct{})
	const size = 96
	go func() {
		defer close(done)
		win.Animate(int32(d.Width-size-40), int32(d.Height-size-80), size, stop)
	}()
	t.Cleanup(func() { close(stop); <-done })
	time.Sleep(200 * time.Millisecond)
}

func TestDuplicatePrimary(t *testing.T) {
	src, err := OpenDisplay("", Options{})
	if err != nil {
		t.Skipf("no duplicable display: %v", err)
	}
	defer src.Close()
	w, h := src.Size()
	t.Logf("duplicating on %s: %dx%d @ %.2f Hz %s", src.Adapter().Name, w, h, src.Refresh(), src.Format())
	animate(t, capture.Display{Width: w, Height: h})

	frames := 0
	var lag []time.Duration
	var ms0, ms1 runtime.MemStats
	for i := 0; i < 60; i++ {
		if i == 10 {
			runtime.ReadMemStats(&ms0)
		}
		f, err := src.Next(100 * time.Millisecond)
		if errors.Is(err, capture.ErrTimeout) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		frames++
		if f.Surface == 0 || f.Width != w || f.Height != h || f.Adapter != src.Adapter().LUID {
			t.Fatalf("bad frame %+v", f)
		}
		if f.Presented <= 0 || f.Acquired < f.Presented {
			t.Fatalf("frame stamps out of order: presented %v acquired %v", f.Presented, f.Acquired)
		}
		lag = append(lag, f.Acquired-f.Presented)
	}
	runtime.ReadMemStats(&ms1)
	if frames < 20 {
		t.Fatalf("only %d frames in 60 waits", frames)
	}
	t.Logf("%d frames; present->acquire first %v last %v; %d mallocs over the last 50 waits",
		frames, lag[0], lag[len(lag)-1], ms1.Mallocs-ms0.Mallocs)

	if err := src.Recover(2*time.Second, 50*time.Millisecond); err != nil {
		t.Fatalf("recover a live source: %v", err)
	}
	for i := 0; i < 20; i++ {
		if _, err := src.Next(100 * time.Millisecond); err == nil {
			return
		} else if !errors.Is(err, capture.ErrTimeout) {
			t.Fatal(err)
		}
	}
	t.Fatal("no frame after Recover")
}
