package player

import (
	"sync"
	"testing"
	"time"

	"github.com/planckdotgg/planck/pkg/capture"
	"github.com/planckdotgg/planck/pkg/decode"
)

// fakeDecoder hands out numbered pictures and tracks which are still out.
type fakeDecoder struct {
	mu       sync.Mutex
	next     uintptr
	out      map[uintptr]bool
	released int
	closed   bool
}

func (d *fakeDecoder) Decode(_ []byte, pts int64) (decode.Frame, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.next++
	d.out[d.next] = true
	return decode.Frame{Surface: d.next, Ref: d.next, Format: capture.FormatNV12, Width: 64, Height: 64, PTS: pts}, nil
}

func (d *fakeDecoder) Release(f decode.Frame) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.out[f.Ref] {
		panic("picture released twice or never decoded")
	}
	delete(d.out, f.Ref)
	d.released++
}

func (d *fakeDecoder) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.closed = true
	return nil
}

func (d *fakeDecoder) outstanding() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.out)
}

type countObserver struct {
	mu        sync.Mutex
	presented []int64
}

func (o *countObserver) Presented(t Timing, err error) {
	o.mu.Lock()
	o.presented = append(o.presented, t.PTS)
	o.mu.Unlock()
}

func (o *countObserver) Glass(int64, time.Duration, time.Duration) {}

// TestStalledPresentDoesNotBlockDecode holds the present thread inside a
// present, as nextDrawable does with both drawables in flight, and checks
// that decoding goes on, that only the newest picture is shown next, and
// that every picture goes back to the decoder.
func TestStalledPresentDoesNotBlockDecode(t *testing.T) {
	dec := &fakeDecoder{out: map[uintptr]bool{}}
	obs := &countObserver{}
	stall := make(chan struct{})
	entered := make(chan struct{}, 64)
	var onScreen sync.Map
	show := func(f decode.Frame) error {
		entered <- struct{}{}
		if f.PTS == 0 {
			<-stall
		}
		onScreen.Store(f.Ref, true)
		return nil
	}
	p := start(dec, 16, show, func(int, int) error { return nil })
	p.SetObserver(obs)

	if _, err := p.Submit(nil, 0); err != nil {
		t.Fatal(err)
	}
	<-entered
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := int64(1); i <= 50; i++ {
			if _, err := p.Submit(nil, i); err != nil {
				t.Error(err)
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Submit blocked behind a stalled present")
	}
	if n := dec.outstanding(); n != 2 {
		t.Fatalf("%d pictures out while the present stalls, want 2 (the one in present and the mailbox)", n)
	}
	close(stall)
	deadline := time.Now().Add(2 * time.Second)
	for p.Stats().Presented < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	st := p.Stats()
	if st.Pictures != 51 || st.Presented != 2 || st.Superseded != 49 {
		t.Fatalf("stats %+v, want 51 decoded, 2 presented, 49 superseded", st)
	}
	obs.mu.Lock()
	got := append([]int64(nil), obs.presented...)
	obs.mu.Unlock()
	if len(got) != 2 || got[0] != 0 || got[1] != 50 {
		t.Fatalf("presented %v, want [0 50]", got)
	}
	if n := dec.outstanding(); n != 1 {
		t.Fatalf("%d pictures out with one on screen, want 1", n)
	}
	if err := p.Resize(10, 10); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if n := dec.outstanding(); n != 0 || !dec.closed {
		t.Fatalf("after Close: %d pictures out, decoder closed %v", n, dec.closed)
	}
	if _, err := p.Submit(nil, 99); err == nil {
		t.Fatal("Submit after Close succeeded")
	}
	if n := dec.outstanding(); n != 0 {
		t.Fatalf("Submit after Close kept %d pictures", n)
	}
	if err := p.Resize(10, 10); err == nil {
		t.Fatal("Resize after Close succeeded")
	}
}

// TestCloseReleasesUnshown closes with a picture still in the mailbox.
func TestCloseReleasesUnshown(t *testing.T) {
	for range 200 {
		dec := &fakeDecoder{out: map[uintptr]bool{}}
		p := start(dec, 0, func(decode.Frame) error { return nil }, func(int, int) error { return nil })
		for i := range int64(5) {
			if _, err := p.Submit(nil, i); err != nil {
				t.Fatal(err)
			}
		}
		if err := p.Close(); err != nil {
			t.Fatal(err)
		}
		if n := dec.outstanding(); n != 0 {
			t.Fatalf("%d pictures out after Close", n)
		}
	}
}
