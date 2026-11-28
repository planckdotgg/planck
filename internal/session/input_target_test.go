package session

import (
	"testing"

	"github.com/planckdotgg/planck/pkg/capture"
	"github.com/planckdotgg/planck/pkg/input"
)

func rect(x, y, w, h int) input.Rect { return input.Rect{X: x, Y: y, W: w, H: h} }

func TestInputArea(t *testing.T) {
	primary := capture.Display{ID: `\\.\DISPLAY1`, Width: 1920, Height: 1080, Primary: true}
	vdd := capture.Display{ID: `\\.\DISPLAY32`, X: 1920, Width: 5120, Height: 1440}
	left := capture.Display{ID: `\\.\DISPLAY33`, X: -2560, Y: -360, Width: 2560, Height: 1440}
	for _, c := range []struct {
		name          string
		d             capture.Display
		all           []capture.Display
		wantD, wantDk input.Rect
	}{
		{"primary alone", primary, []capture.Display{primary}, rect(0, 0, 1920, 1080), rect(0, 0, 1920, 1080)},
		{"virtual display right", vdd, []capture.Display{primary, vdd}, rect(1920, 0, 5120, 1440), rect(0, 0, 7040, 1440)},
		{"primary beside a virtual display", primary, []capture.Display{primary, vdd}, rect(0, 0, 1920, 1080), rect(0, 0, 7040, 1440)},
		{"display at negative coordinates", left, []capture.Display{primary, left}, rect(-2560, -360, 2560, 1440), rect(-2560, -360, 4480, 1440)},
		// A list taken before a game's 5120x1440 to 3840x2160 switch settled.
		{"lone display, stale list", capture.Display{ID: vdd.ID, Width: 3840, Height: 2160},
			[]capture.Display{{ID: vdd.ID, Width: 5120, Height: 1440}}, rect(0, 0, 3840, 2160), rect(0, 0, 3840, 2160)},
		{"beside the primary, stale list", capture.Display{ID: vdd.ID, X: 1920, Width: 3840, Height: 2160},
			[]capture.Display{primary, vdd}, rect(1920, 0, 3840, 2160), rect(0, 0, 5760, 2160)},
	} {
		d, dk := inputArea(c.d, c.all)
		if d != c.wantD || dk != c.wantDk {
			t.Errorf("%s: display %+v desktop %+v, want %+v %+v", c.name, d, dk, c.wantD, c.wantDk)
		}
	}
}

type targetInjector struct {
	display, desktop input.Rect
	calls            int
}

func (i *targetInjector) Inject(input.Event) error { return nil }
func (i *targetInjector) Close() error             { return nil }
func (i *targetInjector) SetTarget(d, dk input.Rect) {
	i.display, i.desktop = d, dk
	i.calls++
}

// TestTargetInputFollowsThePipe: each pipe opened, as for a new virtual
// display or one resized, points absolute moves at its display.
func TestTargetInputFollowsThePipe(t *testing.T) {
	inj := &targetInjector{}
	h := &Host{inject: inj, logf: t.Logf}
	h.targetInput(VideoInfo{Display: rect(1920, 0, 5120, 1440), Desktop: rect(0, 0, 7040, 1440)})
	h.targetInput(VideoInfo{Display: rect(1920, 0, 3456, 2234), Desktop: rect(0, 0, 5376, 2234)})
	if inj.calls != 2 || inj.display != (rect(1920, 0, 3456, 2234)) || inj.desktop != (rect(0, 0, 5376, 2234)) {
		t.Fatalf("after a resize: %d calls, display %+v desktop %+v", inj.calls, inj.display, inj.desktop)
	}
	h.targetInput(VideoInfo{Source: "synthetic"})
	if inj.display != (input.Rect{}) {
		t.Fatalf("a synthetic source left the target at %+v", inj.display)
	}
	(&Host{logf: t.Logf}).targetInput(VideoInfo{Display: input.Rect{W: 1}})
}
