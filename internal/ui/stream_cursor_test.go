package ui

import (
	"slices"
	"testing"

	mui "github.com/egoist/mygo/ui"
)

// fakeCursor is the client's mouse lock as the overlay drives it.
type fakeCursor struct {
	locked, menuOpen bool
	sets             []bool
	ended            int
}

func (f *fakeCursor) CursorLocked() bool { return f.locked }
func (f *fakeCursor) SetCursorLocked(on bool) {
	f.locked = on
	f.sets = append(f.sets, on)
}
func (f *fakeCursor) SetMenuOpen(open bool) { f.menuOpen = open }
func (f *fakeCursor) StreamEnded()          { f.ended++; f.locked = false }

func cursorTester(t *testing.T) (*App, *mui.Tester, *fakeCursor, *fakeBackend) {
	f := streamFake()
	a, tt, _ := newStreamTester(t, f)
	cur := &fakeCursor{}
	a.SetCursor(cur)
	tt.Frame()
	return a, tt, cur, f
}

// TestCursorShortcutTogglesLock: Ctrl+Alt+G locks the mouse, then
// releases it, each time saying how to undo it.
func TestCursorShortcutTogglesLock(t *testing.T) {
	_, tt, cur, _ := cursorTester(t)
	tt.Key(mui.Ctrl|mui.Alt, mui.KeyG)
	tt.Frame()
	if !cur.locked || !tt.HasText("Cursor locked") || !tt.HasText("Ctrl Alt G") || !tt.HasText("to release") {
		t.Fatalf("lock: locked %v, texts %q", cur.locked, tt.Texts())
	}
	tt.Key(mui.Ctrl|mui.Alt, mui.KeyG)
	tt.Frame()
	if cur.locked || !tt.HasText("Cursor released") || !tt.HasText("or click the stream to lock") {
		t.Fatalf("release: locked %v, texts %q", cur.locked, tt.Texts())
	}
	tt.Advance(toastIn)
	snapshot(t, tt, "cursor-released-toast")
	if want := []bool{true, false}; !slices.Equal(cur.sets, want) {
		t.Fatalf("lock calls %v, want %v", cur.sets, want)
	}
	tt.Advance(toastHold + toastOut)
	if tt.HasText("Cursor released") {
		t.Fatal("the toast outlived its time")
	}
}

// TestGameLockToastsOnce: the first time a game locks the mouse the toast
// says how to release it; later locks in the same stream pass quietly.
func TestGameLockToastsOnce(t *testing.T) {
	_, tt, cur, _ := cursorTester(t)
	cur.locked = true
	tt.Frame()
	if !tt.HasText("Cursor locked") {
		t.Fatalf("no toast on the game's lock: %q", tt.Texts())
	}
	tt.Advance(toastIn + toastHold + toastOut)
	cur.locked = false
	tt.Frame()
	cur.locked = true
	tt.Frame()
	if tt.HasText("Cursor locked") {
		t.Fatal("the game's second lock toasted again")
	}
}

// TestMenuFreesCursor: the menu open frees the mouse, and so does the
// overlay hidden; the stream's end forgets the lock.
func TestMenuFreesCursor(t *testing.T) {
	_, tt, cur, f := cursorTester(t)
	cur.locked = true
	openMenu(t, tt)
	if !cur.menuOpen {
		t.Fatal("the menu open did not free the mouse")
	}
	if !shows(tt, "Lock cursor") || !tt.HasText("Locks as this menu closes") || !tt.HasText("Ctrl Alt G") {
		t.Fatalf("no Lock cursor row: %q", tt.Texts())
	}
	snapshot(t, tt, "menu-lock-cursor")
	click(t, tt, "Lock cursor")
	if cur.locked || !shows(tt, "Lock cursor") {
		t.Fatalf("the row: locked %v, menu shown %v", cur.locked, shows(tt, "Lock cursor"))
	}
	tt.Key(0, mui.KeyEscape)
	tt.Frame()
	if cur.menuOpen {
		t.Fatal("the menu closed and the mouse stayed free")
	}
	tt.Key(mui.Ctrl|mui.Alt, mui.KeyO)
	tt.Frame()
	tt.Key(mui.Ctrl|mui.Alt, mui.KeyP)
	tt.Frame()
	if cur.menuOpen {
		t.Fatal("hiding the overlay left the mouse held off")
	}
	f.Disconnect()
	tt.Frame()
	if cur.ended != 1 {
		t.Fatalf("stream ends told: %d, want 1", cur.ended)
	}
}
