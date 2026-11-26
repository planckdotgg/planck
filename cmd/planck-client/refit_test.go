//go:build darwin || windows || linux

package main

import (
	"sync/atomic"
	"testing"
	"time"
)

// TestRefitWithoutNewPicture: a change of the window's size, or of the
// present path, resizes the surface and shows the picture again at once,
// with no picture from the host, as one sharing a still desktop sends
// none for seconds.
func TestRefitWithoutNewPicture(t *testing.T) {
	v := &videoView{logf: t.Logf}
	resized := make(chan struct{}, 16)
	s := &surfaceSink{v: v, resize: func() error { resized <- struct{}{}; return nil }}
	v.cur.Store(s)

	v.refit()
	select {
	case <-resized:
	case <-time.After(2 * time.Second):
		t.Fatal("no resize without a new picture")
	}
	if v.resize.Load() {
		t.Fatal("resize still pending after the refit")
	}
}

// TestRefitKeepsDecodeGoing: the decode goroutine does not wait for a
// resize under way, and a size that changed during it is resized after.
func TestRefitKeepsDecodeGoing(t *testing.T) {
	v := &videoView{logf: t.Logf}
	release := make(chan struct{})
	var calls atomic.Int32
	s := &surfaceSink{v: v, resize: func() error {
		if calls.Add(1) == 1 {
			<-release
		}
		return nil
	}}
	v.cur.Store(s)
	v.refit()
	deadline := time.Now().Add(2 * time.Second)
	for calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	v.refit()

	done := make(chan struct{})
	go func() { s.refit(false); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("decode waited for a resize under way")
	}
	close(release)
	for calls.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("%d resizes, want 2: the size that changed mid-resize was lost", n)
	}
}
