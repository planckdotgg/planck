package main

import (
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

type sizeLog struct {
	mu  sync.Mutex
	got [][2]int
}

func (l *sizeLog) send(w, h int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.got = append(l.got, [2]int{w, h})
}

func (l *sizeLog) sent() [][2]int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([][2]int(nil), l.got...)
}

// TestViewResizerCoalescesDrag: a drag's sizes send once, the last, when
// they stop for the settle time.
func TestViewResizerCoalescesDrag(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var l sizeLog
		r := &viewResizer{delay: resizeSettle, send: l.send}
		for i := range 20 {
			r.changed(1280+10*i, 800)
			time.Sleep(16 * time.Millisecond)
		}
		synctest.Wait()
		if got := l.sent(); len(got) != 0 {
			t.Fatalf("sent during the drag: %v", got)
		}
		time.Sleep(resizeSettle)
		synctest.Wait()
		if got := l.sent(); len(got) != 1 || got[0] != [2]int{1470, 800} {
			t.Fatalf("after the drag: %v, want one 1470x800", got)
		}
		r.changed(1600, 900)
		time.Sleep(resizeSettle + time.Millisecond)
		synctest.Wait()
		if got := l.sent(); len(got) != 2 || got[1] != [2]int{1600, 900} {
			t.Fatalf("a second drag: %v", got)
		}
	})
}

// TestViewResizerFullScreenAtOnce: entering full screen sends its size at
// once and drops the size a drag left settling.
func TestViewResizerFullScreenAtOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var l sizeLog
		r := &viewResizer{delay: resizeSettle, send: l.send}
		r.changed(1400, 900)
		time.Sleep(50 * time.Millisecond)
		r.now(5120, 1440)
		if got := l.sent(); len(got) != 1 || got[0] != [2]int{5120, 1440} {
			t.Fatalf("full screen: %v", got)
		}
		time.Sleep(2 * resizeSettle)
		synctest.Wait()
		if got := l.sent(); len(got) != 1 {
			t.Fatalf("the drag's size came after full screen: %v", got)
		}
	})
}
