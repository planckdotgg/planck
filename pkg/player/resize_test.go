package player

import (
	"sync"
	"testing"
	"time"

	"github.com/planckdotgg/planck/pkg/decode"
)

// TestResizeShowsThePictureAgain: Resize, from a goroutine of its own
// while pictures still come, resizes on the present thread and shows the
// picture on screen again at the new size, with no new picture needed.
func TestResizeShowsThePictureAgain(t *testing.T) {
	dec := &fakeDecoder{out: map[uintptr]bool{}}
	var mu sync.Mutex
	size := [2]int{100, 100}
	var shownAt [][2]int
	show := func(f decode.Frame) error {
		mu.Lock()
		shownAt = append(shownAt, size)
		mu.Unlock()
		return nil
	}
	resize := func(w, h int) error {
		mu.Lock()
		size = [2]int{w, h}
		mu.Unlock()
		return nil
	}
	p := start(dec, 0, show, resize)
	defer p.Close()
	if _, err := p.Submit(nil, 0); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for p.Stats().Presented < 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}

	if err := p.Resize(300, 100); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	last := shownAt[len(shownAt)-1]
	mu.Unlock()
	if last != [2]int{300, 100} {
		t.Fatalf("after Resize with no new picture: last shown at %v, want 300 x 100", last)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := int64(1); i <= 20; i++ {
			if _, err := p.Submit(nil, i); err != nil {
				t.Error(err)
			}
		}
	}()
	if err := p.Resize(200, 100); err != nil {
		t.Fatal(err)
	}
	<-done
}
