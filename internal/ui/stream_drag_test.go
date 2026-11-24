package ui

import (
	"testing"
	"time"

	mui "github.com/egoist/mygo/ui"
	"github.com/planckdotgg/planck/internal/ui/theme"
)

// dragProbe reads the stream view as the client's input forwarder does:
// the pointer's place and the button held, every frame.
type dragProbe struct {
	frames []dragFrame
}

type dragFrame struct {
	x, y          float32
	over, pressed bool
}

func (p *dragProbe) view(c *mui.Context) {
	c.Key("probe-stream-input")
	el := mui.Box(c).Fill().Label("Stream")
	x, y, over := el.PointerPosition()
	_, pressed := el.PressedButton()
	p.frames = append(p.frames, dragFrame{x, y, over, pressed})
}

// TestStreamDragReachesTheStream: a drag on the stream, beside the
// overlay's button and with the metrics window shown, reaches the stream
// view whole: held from press to release, the pointer followed in every
// frame between.
func TestStreamDragReachesTheStream(t *testing.T) {
	if err := theme.RegisterFonts(); err != nil {
		t.Fatal(err)
	}
	f := streamFake()
	f.layout = OverlayLayout{Placed: true, ButtonX: 0.5, ButtonY: 0.5, Metrics: true}
	a := NewApp(f)
	p := &dragProbe{}
	a.SetStreamView(p.view)
	tt := mui.NewTester(a.View, testWidth, testHeight)
	tt.Move(300, 700)
	tt.Frame()
	p.frames = nil
	tt.Press(300, 700)
	tt.Frame()
	const steps = 10
	for i := 1; i <= steps; i++ {
		tt.Move(300+float32(i)*40, 700-float32(i)*20)
		tt.Advance(16 * time.Millisecond)
	}
	held := p.frames
	tt.Release(700, 500)
	tt.Frame()

	if len(held) < steps+1 || !held[0].pressed {
		t.Fatalf("press not seen: %+v", held)
	}
	for i := 1; i < len(held); i++ {
		if !held[i].pressed || !held[i].over {
			t.Fatalf("frame %d of the drag: %+v, want held over the stream", i, held[i])
		}
	}
	moved := 0
	for i := 1; i < len(held); i++ {
		if held[i].x != held[i-1].x {
			moved++
		}
	}
	if moved < steps {
		t.Fatalf("the stream saw the pointer move in %d frames of %d", moved, steps)
	}
	if last := held[len(held)-1]; last.x != 700 || last.y != 500 {
		t.Fatalf("last drag frame at %v, %v, want 700, 500", last.x, last.y)
	}
	if end := p.frames[len(p.frames)-1]; end.pressed {
		t.Fatal("release not seen")
	}
	if a.ovl.menuOpen || a.ovl.off != (mui.Point{}) {
		t.Fatal("the overlay took part in the drag")
	}
}
