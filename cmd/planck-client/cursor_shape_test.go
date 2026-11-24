package main

import (
	"fmt"
	"slices"
	"testing"

	mui "github.com/egoist/mygo/ui"

	"github.com/planckdotgg/planck/pkg/input"
	"github.com/planckdotgg/planck/pkg/wire"
)

// fakeHostCursor is the system's cursor as the lock sets it. other says
// the UI set its own since, which set then replaces.
type fakeHostCursor struct {
	calls []string
	other bool
}

func (f *fakeHostCursor) set(s *wire.CursorShape) bool {
	name := "set none"
	if s != nil {
		name = fmt.Sprintf("set %x", s.ID)
	}
	f.calls = append(f.calls, name)
	again := f.other
	f.other = false
	return again
}

func (f *fakeHostCursor) unset() { f.calls = append(f.calls, "unset") }
func (f *fakeHostCursor) close() { f.calls = append(f.calls, "close") }

func (f *fakeHostCursor) take() []string {
	c := f.calls
	f.calls = nil
	return c
}

func newCursorLock(t *testing.T) (*pointerLock, *fakePointer, *fakeHostCursor, *[]input.Event) {
	l, sys, sent := newTestLock(t)
	cur := &fakeHostCursor{}
	l.cursor = cur
	return l, sys, cur, sent
}

var arrowShape = wire.CursorShape{ID: 0xa, Width: 1, Height: 1, DPI: 96, RGBA: []byte{0, 0, 0, 255}}

func wantPicture(t *testing.T, l *pointerLock, cur *fakeHostCursor, over bool, want pictureCursor, calls ...string) {
	t.Helper()
	if got := l.pictureCursor(over); got != want {
		t.Fatalf("picture cursor %d, want %d", got, want)
	}
	if got := cur.take(); !slices.Equal(got, calls) {
		t.Fatalf("cursor calls %q, want %q", got, calls)
	}
}

// TestHiddenCursorStaysFree: the host hiding its cursor without capturing
// the mouse, as over a YouTube video, neither locks nor warps the local
// pointer: it stays free, hidden over the picture alone.
func TestHiddenCursorStaysFree(t *testing.T) {
	l, sys, cur, sent := newCursorLock(t)
	l.setHost(wire.CursorState{Visible: true, HasPos: true, X: 100, Y: 100})
	l.setShape(arrowShape)
	l.frame(true, testPicture)
	wantPicture(t, l, cur, true, cursorHost, "set a")

	l.setHost(wire.CursorState{HasPos: true, X: 30000, Y: 30000})
	l.frame(true, testPicture)
	wantCalls(t, sys)
	if l.held() != pointerFree || l.locked() || sys.hides != 0 {
		t.Fatalf("held %d locked %v hidden %d", l.held(), l.locked(), sys.hides)
	}
	wantPicture(t, l, cur, true, cursorHidden, "set none")
	l.motion(5, 5)
	if len(*sent) != 0 {
		t.Fatalf("motion sent while free: %+v", *sent)
	}
	// Off the picture, onto the overlay or a bar, the local cursor shows.
	wantPicture(t, l, cur, false, cursorLocal, "unset")
	wantPicture(t, l, cur, false, cursorLocal)
	wantPicture(t, l, cur, true, cursorHidden, "set none")
	// The video stops and the cursor shows again, as the host draws it.
	l.setHost(wire.CursorState{Visible: true, HasPos: true, X: 30000, Y: 30000})
	l.frame(true, testPicture)
	wantCalls(t, sys)
	wantPicture(t, l, cur, true, cursorHost, "set a")
}

// TestHostCursorShape: over the picture the host's cursor shows as it last
// sent it, and the window's own elsewhere, with the menu open, without
// the focus, from a host that sends no shape, and once the stream ends.
// The UI setting its own over it asks for a frame that sets it again.
func TestHostCursorShape(t *testing.T) {
	l, _, cur, _ := newCursorLock(t)
	frames := 0
	l.changed = func() { frames++ }
	l.setHost(wire.CursorState{Visible: true})
	l.frame(true, testPicture)
	wantPicture(t, l, cur, true, cursorLocal)

	l.setShape(arrowShape)
	frames = 0
	cur.other = true
	wantPicture(t, l, cur, true, cursorHost, "set a")
	if frames != 1 {
		t.Fatalf("%d frames asked after setting over the UI's cursor", frames)
	}
	beam := arrowShape
	beam.ID = 0xb
	l.setShape(beam)
	wantPicture(t, l, cur, true, cursorHost, "set b")

	l.setMenuOpen(true)
	wantPicture(t, l, cur, true, cursorLocal, "unset")
	l.setMenuOpen(false)
	wantPicture(t, l, cur, true, cursorHost, "set b")
	l.frame(false, testPicture)
	wantPicture(t, l, cur, true, cursorLocal, "unset")
	l.frame(true, testPicture)
	wantPicture(t, l, cur, true, cursorHost, "set b")
	l.reset()
	if got := cur.take(); !slices.Equal(got, []string{"unset"}) {
		t.Fatalf("reset: %q", got)
	}
	l.frame(true, testPicture)
	wantPicture(t, l, cur, true, cursorLocal)
	l.close()
	if got := cur.take(); !slices.Equal(got, []string{"close"}) {
		t.Fatalf("close: %q", got)
	}
}

// TestCapturedCursor: a game capturing the mouse locks it relative and
// hidden by the lock, not the host's cursor; letting go of it shows the
// local cursor over the picture, to find the pointer with, and a confined
// cursor is the host's.
func TestCapturedCursor(t *testing.T) {
	l, sys, cur, _ := newCursorLock(t)
	l.setShape(arrowShape)
	l.setHost(wire.CursorState{Captured: true})
	l.frame(true, testPicture)
	wantCalls(t, sys, "warp 400,202.5", "hold 1", "hide")
	wantPicture(t, l, cur, true, cursorLocal)
	l.setLocked(false)
	wantCalls(t, sys, "free", "show")
	wantPicture(t, l, cur, true, cursorLocal)
	if !l.clickPicture() {
		t.Fatal("a click did not lock again")
	}
	wantCalls(t, sys, "warp 400,202.5", "hold 1", "hide")
	l.frame(false, testPicture)
	wantCalls(t, sys, "free", "show")
	wantPicture(t, l, cur, true, cursorLocal)

	l.frame(true, testPicture)
	l.setHost(wire.CursorState{Visible: true, Captured: true, HasPos: true})
	l.frame(true, testPicture)
	l.clickPicture()
	if l.held() != pointerConfined {
		t.Fatalf("held %d", l.held())
	}
	wantPicture(t, l, cur, true, cursorHost, "set a")
	l.reset()
	wantCalls(t, sys, "warp 40,0", "hold 2", "free")
	if got := cur.take(); !slices.Equal(got, []string{"unset"}) {
		t.Fatalf("reset: %q", got)
	}
}

// pictureView is the stream view of f with a button over its top-left
// corner, as the stream's overlay puts one over the picture.
func pictureView(f *inputForwarder) func(c *mui.Context) {
	return func(c *mui.Context) {
		mui.Box(c).Fill().Children(func() {
			f.view(c)
			mui.Box(c).Absolute().Left(100).Top(20).Size(60, 60).Label("Menu")
		})
	}
}

// TestStreamViewHidesUICursor: over the picture the stream view asks the
// UI for no cursor of its own while the host's shows, and gets the UI's
// back over the overlay and the bars, where the local cursor shows.
func TestStreamViewHidesUICursor(t *testing.T) {
	f, _, sent := barsForwarder(1920, 1080)
	sys := &fakePointer{t: t}
	cur := &fakeHostCursor{}
	f.lock = newPointerLock(sys, f.send)
	f.lock.cursor = cur
	tt := mui.NewTester(pictureView(f), 800, 400)
	f.lock.setHost(wire.CursorState{HasPos: true, X: 1, Y: 1})
	f.lock.setShape(arrowShape)
	for _, c := range []struct {
		name string
		x, y float32
		ui   mui.Cursor
		last string
	}{
		{"the picture, the host's cursor hidden", 400, 200, mui.CursorNone, "set none"},
		{"the overlay's button", 120, 40, mui.CursorDefault, "unset"},
		{"the picture again", 300, 300, mui.CursorNone, "set none"},
		{"a bar", 20, 200, mui.CursorDefault, "unset"},
	} {
		*sent = nil
		tt.Move(c.x, c.y)
		tt.Frame()
		tt.Frame()
		calls := cur.take()
		if tt.Cursor() != c.ui || len(calls) == 0 || calls[len(calls)-1] != c.last {
			t.Fatalf("%s: UI cursor %d, calls %q", c.name, tt.Cursor(), calls)
		}
		if sys.mode != pointerFree || sys.hides != 0 {
			t.Fatalf("%s: held %d, hidden %d", c.name, sys.mode, sys.hides)
		}
		if c.ui == mui.CursorNone && len(ofType(*sent, input.TypeMouseMoveAbs)) == 0 {
			t.Fatalf("%s: no absolute move sent", c.name)
		}
	}
	f.lock.setHost(wire.CursorState{Visible: true, HasPos: true, X: 1, Y: 1})
	tt.Move(400, 210)
	tt.Frame()
	tt.Frame()
	if calls := cur.take(); tt.Cursor() != mui.CursorNone || calls[len(calls)-1] != "set a" {
		t.Fatalf("host's shape: UI cursor %d, calls %q", tt.Cursor(), calls)
	}
}

// TestCursorCacheFrees: the cache keeps the most recently used cursors,
// frees each one it drops once, and the rest as it closes.
func TestCursorCacheFrees(t *testing.T) {
	freed := map[int]int{}
	c := cursorCache[uint64, int]{free: func(v int) { freed[v]++ }}
	made := 0
	get := func(k uint64) int {
		v, ok := c.get(k, func() (int, bool) { made++; return int(k), true })
		if !ok || v != int(k) {
			t.Fatalf("get %d = %d, %v", k, v, ok)
		}
		return v
	}
	for k := range uint64(wire.CursorCacheSize) {
		get(k)
	}
	get(0)
	get(100)
	if made != wire.CursorCacheSize+1 || len(freed) != 1 || freed[1] != 1 {
		t.Fatalf("made %d, freed %v: want key 1, the least recent, freed", made, freed)
	}
	if _, ok := c.get(200, func() (int, bool) { return 0, false }); ok {
		t.Fatal("a failed make was kept")
	}
	c.close()
	if len(freed) != wire.CursorCacheSize+1 {
		t.Fatalf("freed %d cursors in all", len(freed))
	}
	for k, n := range freed {
		if n != 1 {
			t.Fatalf("cursor %d freed %d times", k, n)
		}
	}
}

// TestScaleRGBA: doubling keeps a solid image solid and a clear one clear,
// with no dark fringe where they meet; scaling by one changes nothing.
func TestScaleRGBA(t *testing.T) {
	px := []byte{255, 255, 255, 255, 0, 0, 0, 0}
	out := scaleRGBA(px, 2, 1, 4, 2)
	for x := range 4 {
		p := out[x*4:][:4]
		if p[3] > 0 && (p[0] != 255 || p[1] != 255 || p[2] != 255) {
			t.Errorf("pixel %d = %v: a fringe", x, p)
		}
	}
	if out[3] != 255 || out[15] != 0 {
		t.Errorf("edges %v", out[:16])
	}
	if same := scaleRGBA(px, 2, 1, 2, 1); !slices.Equal(same, px) {
		t.Errorf("scaled by one: %v", same)
	}
	if k := cursorScale(&wire.CursorShape{DPI: 144}, 96); k != 96.0/144 {
		t.Errorf("scale %g", k)
	}
}
