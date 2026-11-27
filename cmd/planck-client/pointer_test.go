package main

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/planckdotgg/planck/pkg/input"
	"github.com/planckdotgg/planck/pkg/wire"
)

// fakePointer is the system pointer as the lock drives it, which no test
// may touch for real.
type fakePointer struct {
	t      *testing.T
	calls  []string
	hides  int
	mode   pointerMode
	area   rect
	warped [2]float32
}

func (f *fakePointer) hide() { f.hides++; f.calls = append(f.calls, "hide") }

func (f *fakePointer) show() {
	f.hides--
	if f.hides < 0 {
		f.t.Fatal("cursor shown more times than hidden")
	}
	f.calls = append(f.calls, "show")
}

func (f *fakePointer) hold(m pointerMode, area rect, at spot) {
	if m == pointerFree {
		f.t.Fatal("held free")
	}
	f.mode, f.area = m, area
	f.calls = append(f.calls, fmt.Sprintf("hold %d", m)+f.at(at))
}

func (f *fakePointer) free(at spot) {
	if f.mode == pointerFree {
		f.t.Fatal("freed a pointer not held")
	}
	f.mode = pointerFree
	f.calls = append(f.calls, "free"+f.at(at))
}

func (f *fakePointer) at(at spot) string {
	if !at.set {
		return ""
	}
	f.warped = [2]float32{at.x, at.y}
	return fmt.Sprintf(" at %g,%g", at.x, at.y)
}

func (f *fakePointer) take() []string {
	c := f.calls
	f.calls = nil
	return c
}

var testPicture = rect{X: 40, Y: 0, W: 720, H: 405}

func newTestLock(t *testing.T) (*pointerLock, *fakePointer, *[]input.Event) {
	sys := &fakePointer{t: t}
	var sent []input.Event
	l := newPointerLock(sys, func(ev input.Event) { sent = append(sent, ev) })
	l.frame(true, testPicture)
	return l, sys, &sent
}

func wantCalls(t *testing.T, sys *fakePointer, want ...string) {
	t.Helper()
	if got := sys.take(); !slices.Equal(got, want) {
		t.Fatalf("calls %q, want %q", got, want)
	}
}

// TestGameCaptureLocksRelative: a game hiding the host's cursor locks the
// mouse: the local cursor hides, holds still at the picture's centre and
// the mouse's motion goes to the host, fractions carried over.
func TestGameCaptureLocksRelative(t *testing.T) {
	l, sys, sent := newTestLock(t)
	wantCalls(t, sys)
	l.setHost(wire.CursorState{Visible: true})
	l.frame(true, testPicture)
	wantCalls(t, sys)

	l.setHost(wire.CursorState{Captured: true})
	l.frame(true, testPicture)
	wantCalls(t, sys, "hold 1 at 400,202.5", "hide")
	if !l.locked() || l.held() != pointerRelative {
		t.Fatalf("locked %v held %d", l.locked(), l.held())
	}
	l.motion(0.6, -2.5)
	l.motion(0.6, 0.4)
	l.motion(70000, 0)
	want := []input.Event{
		{Type: input.TypeMouseMoveRel, MouseRel: input.MouseMoveRel{DY: -2}},
		{Type: input.TypeMouseMoveRel, MouseRel: input.MouseMoveRel{DX: 1}},
		{Type: input.TypeMouseMoveRel, MouseRel: input.MouseMoveRel{DX: 32767}},
		{Type: input.TypeMouseMoveRel, MouseRel: input.MouseMoveRel{DX: 32767}},
		{Type: input.TypeMouseMoveRel, MouseRel: input.MouseMoveRel{DX: 4466}},
	}
	if !slices.Equal(*sent, want) {
		t.Fatalf("sent %+v\nwant %+v", *sent, want)
	}
	l.frame(true, testPicture)
	wantCalls(t, sys)
}

// TestShortcutReleasesAndClickRelocks: the user's release frees and shows
// the cursor and stops the motion; a click on the picture takes the lock
// again and is the lock's, not the game's.
func TestShortcutReleasesAndClickRelocks(t *testing.T) {
	l, sys, sent := newTestLock(t)
	l.setHost(wire.CursorState{Captured: true})
	l.frame(true, testPicture)
	sys.take()

	l.setLocked(false)
	wantCalls(t, sys, "free", "show")
	if l.locked() {
		t.Fatal("still locked after the release")
	}
	l.motion(5, 5)
	if len(*sent) != 0 {
		t.Fatalf("motion sent while released: %+v", *sent)
	}
	l.frame(true, testPicture)
	wantCalls(t, sys)

	if !l.clickPicture() {
		t.Fatal("a click on the picture did not take the lock again")
	}
	wantCalls(t, sys, "hold 1 at 400,202.5", "hide")
	if l.clickPicture() {
		t.Fatal("a click while locked was taken by the lock")
	}
	l.setLocked(false)
	l.setLocked(true)
	if l.held() != pointerRelative {
		t.Fatal("the shortcut did not lock again")
	}
}

// TestManualLock: without a capture the user locks and releases the
// mouse, and a click does not lock what the user released.
func TestManualLock(t *testing.T) {
	l, sys, _ := newTestLock(t)
	l.setLocked(true)
	wantCalls(t, sys, "hold 1 at 400,202.5", "hide")
	l.setLocked(false)
	wantCalls(t, sys, "free", "show")
	if l.clickPicture() {
		t.Fatal("a click locked a mouse the user released")
	}
	// A host that shows its cursor keeps it visible and confined instead.
	l.setHost(wire.CursorState{Visible: true, HasPos: true, X: 65535, Y: 0})
	l.setLocked(true)
	wantCalls(t, sys, "hold 2")
	if sys.hides != 0 {
		t.Fatal("a confined cursor hid")
	}
}

// TestFocusLossReleasesUntilClick: the focus leaving releases the lock at
// once; coming back keeps it released until a click or the shortcut.
func TestFocusLossReleasesUntilClick(t *testing.T) {
	l, sys, _ := newTestLock(t)
	l.setHost(wire.CursorState{Captured: true})
	l.frame(true, testPicture)
	sys.take()
	l.frame(false, testPicture)
	wantCalls(t, sys, "free", "show")
	if l.clickPicture() {
		t.Fatal("a click took the lock while the window has no focus")
	}
	l.frame(true, testPicture)
	wantCalls(t, sys)
	if !l.clickPicture() {
		t.Fatal("a click did not take the lock back")
	}
	wantCalls(t, sys, "hold 1 at 400,202.5", "hide")
}

// TestMenuHoldsLockOff: the stream's menu frees the cursor while it is
// open, and closing it locks again without a click.
func TestMenuHoldsLockOff(t *testing.T) {
	l, sys, _ := newTestLock(t)
	l.setHost(wire.CursorState{Captured: true})
	sys.take()
	l.frame(true, testPicture)
	sys.take()
	l.setMenuOpen(true)
	wantCalls(t, sys, "free", "show")
	if !l.locked() {
		t.Fatal("the menu counted as a release")
	}
	if l.clickPicture() {
		t.Fatal("a click took the lock with the menu open")
	}
	l.setMenuOpen(false)
	wantCalls(t, sys, "hold 1 at 400,202.5", "hide")
}

// TestGameReleaseWarpsToHostCursor: as the game lets go, the cursor shows
// where the host's is, and the next capture locks again even after a
// release by the user.
func TestGameReleaseWarpsToHostCursor(t *testing.T) {
	l, sys, _ := newTestLock(t)
	l.setHost(wire.CursorState{Captured: true})
	l.frame(true, testPicture)
	sys.take()
	l.setHost(wire.CursorState{Visible: true, HasPos: true, X: 0, Y: 65535})
	l.frame(true, testPicture)
	wantCalls(t, sys, "free at 40,405", "show")

	l.setHost(wire.CursorState{Captured: true})
	l.frame(true, testPicture)
	l.setLocked(false)
	l.setHost(wire.CursorState{Visible: true})
	l.setHost(wire.CursorState{Captured: true})
	sys.take()
	l.frame(true, testPicture)
	wantCalls(t, sys, "hold 1 at 400,202.5", "hide")

	// Visible but confined: the cursor shows, kept on the picture.
	l.setHost(wire.CursorState{Visible: true, Captured: true, HasPos: true, X: 32768, Y: 32768})
	l.frame(true, testPicture)
	wantCalls(t, sys, "hold 2 at 400.0055,202.50308", "show")
}

// TestResizeHoldsTheNewPicture: a held pointer follows the picture.
func TestResizeHoldsTheNewPicture(t *testing.T) {
	l, sys, _ := newTestLock(t)
	l.setLocked(true)
	sys.take()
	bigger := rect{W: 1280, H: 720}
	l.frame(true, bigger)
	wantCalls(t, sys, "hold 1 at 640,360")
	if sys.area != bigger {
		t.Fatalf("held %+v", sys.area)
	}
	l.frame(true, rect{})
	wantCalls(t, sys, "free", "show")
}

// TestResetAndClose: the stream's end forgets the lock, and close gives
// the pointer back from any state, once.
func TestResetAndClose(t *testing.T) {
	l, sys, _ := newTestLock(t)
	l.setHost(wire.CursorState{Captured: true})
	l.frame(true, testPicture)
	sys.take()
	l.reset()
	wantCalls(t, sys, "free", "show")
	l.frame(true, testPicture)
	if l.locked() || len(sys.take()) != 0 {
		t.Fatal("the lock outlived the stream")
	}
	l.setLocked(true)
	sys.take()
	l.close()
	l.close()
	wantCalls(t, sys, "free", "show")
}

// TestLockNeverLeaks drives the lock through random sequences and checks,
// after each step, that the cursor is hidden at most once, only while held
// relative, and that close leaves it shown and free.
func TestLockNeverLeaks(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	states := []wire.CursorState{{Visible: true}, {Captured: true}, {Visible: true, Captured: true, HasPos: true, X: 9, Y: 9}, {}}
	for run := range 200 {
		l, sys, _ := newTestLock(t)
		for step := range 60 {
			switch r.IntN(8) {
			case 0:
				l.setHost(states[r.IntN(len(states))])
			case 1:
				l.setLocked(r.IntN(2) == 0)
			case 2:
				l.setMenuOpen(r.IntN(2) == 0)
			case 3:
				l.frame(r.IntN(3) > 0, testPicture)
			case 4:
				l.clickPicture()
			case 5:
				l.reset()
			case 6:
				l.motion(r.Float64()*10-5, 1)
			case 7:
				l.frame(true, rect{W: float32(r.IntN(3)) * 100, H: 100})
			}
			if sys.hides != 0 && (sys.hides != 1 || sys.mode != pointerRelative) {
				t.Fatalf("run %d step %d: hidden %d times, held %d", run, step, sys.hides, sys.mode)
			}
			if sys.mode != l.held() {
				t.Fatalf("run %d step %d: system holds %d, lock thinks %d", run, step, sys.mode, l.held())
			}
		}
		l.close()
		if sys.hides != 0 || sys.mode != pointerFree {
			t.Fatalf("run %d: close left hidden %d, held %d", run, sys.hides, sys.mode)
		}
	}
}
