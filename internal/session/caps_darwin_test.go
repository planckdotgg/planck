//go:build darwin

package session

import (
	"testing"

	"github.com/ebitengine/purego/objc"
)

// TestScreenDisplayEveryScreen: a window's screen reads as its own
// display's mode, scale and rate, not the main display's.
func TestScreenDisplayEveryScreen(t *testing.T) {
	main := WindowDisplay(0)
	if main.WidthPx <= 0 || main.HeightPx <= 0 || main.Scale <= 0 {
		t.Skipf("no display to read: %+v", main)
	}
	cls := objc.ID(objc.GetClass("NSScreen"))
	screens := cls.Send(objc.RegisterName("screens"))
	for i := range objc.Send[uint](screens, objc.RegisterName("count")) {
		scr := screens.Send(objc.RegisterName("objectAtIndex:"), i)
		id, ok := screenDisplayID(scr)
		if !ok {
			t.Fatalf("screen %d has no display ID", i)
		}
		mode := cgDisplayCopyDisplayMode(id)
		w, h := int(cgDisplayModeGetPixelWidth(mode)), int(cgDisplayModeGetPixelHeight(mode))
		cgDisplayModeRelease(mode)
		got := screenDisplay(scr)
		if got.WidthPx != w || got.HeightPx != h || got.Scale <= 0 || got.MaxHz <= 0 {
			t.Fatalf("screen %d (display %d, %dx%d) read as %+v", i, id, w, h, got)
		}
		if i == 0 && got != main {
			t.Fatalf("screen 0 read as %+v, the main display as %+v", got, main)
		}
		t.Logf("screen %d: display %d %+v", i, id, got)
	}
}
