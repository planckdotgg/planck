//go:build darwin

package input

import "testing"

func TestMacFlags(t *testing.T) {
	cases := []struct {
		mods uint8
		want uint64
	}{
		{0, 0},
		{ModLeftShift, 0x20002},
		{ModRightShift, 0x20004},
		{ModLeftGUI | ModRightAlt, 0x100008 | 0x80040},
		{ModLeftControl | ModRightControl, 0x40000 | 0x0001 | 0x2000},
	}
	for _, c := range cases {
		if got := macFlags(c.mods); got != c.want {
			t.Errorf("macFlags(%08b) = %#x, want %#x", c.mods, got, c.want)
		}
	}
}

// Reads display bounds and the cursor position through the same purego
// bindings the injector uses; posts nothing.
func TestCoreGraphicsReadOnlyBindings(t *testing.T) {
	cgOnce.Do(loadCG)
	if cgErr != nil {
		t.Fatal(cgErr)
	}
	b := mainDisplayBounds()
	if b.Size.W < 100 || b.Size.H < 100 {
		t.Fatalf("main display bounds %+v", b)
	}
	at := cursorLocation()
	t.Logf("main display %+v, cursor %+v, accessibility trusted %v", b, at, axIsProcessTrusted())
}
