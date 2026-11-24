package main

import (
	"testing"
	"testing/synctest"
	"time"
)

type sizeLog struct{ got [][2]int }

func (l *sizeLog) send(w, h int) { l.got = append(l.got, [2]int{w, h}) }

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
		if len(l.got) != 0 {
			t.Fatalf("sent during the drag: %v", l.got)
		}
		time.Sleep(resizeSettle)
		synctest.Wait()
		if len(l.got) != 1 || l.got[0] != [2]int{1470, 800} {
			t.Fatalf("after the drag: %v, want one 1470x800", l.got)
		}
		r.changed(1600, 900)
		time.Sleep(resizeSettle + time.Millisecond)
		synctest.Wait()
		if len(l.got) != 2 || l.got[1] != [2]int{1600, 900} {
			t.Fatalf("a second drag: %v", l.got)
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
		if len(l.got) != 1 || l.got[0] != [2]int{5120, 1440} {
			t.Fatalf("full screen: %v", l.got)
		}
		time.Sleep(2 * resizeSettle)
		synctest.Wait()
		if len(l.got) != 1 {
			t.Fatalf("the drag's size came after full screen: %v", l.got)
		}
	})
}
