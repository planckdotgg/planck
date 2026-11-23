package ui

import (
	"testing"

	mui "github.com/egoist/mygo/ui"
)

// TestStreamViewClearsBackground: with a stream view, a live stream paints
// nothing under the HUD, so a GPU surface beneath the UI's layer shows;
// without one the letterbox well stays opaque.
func TestStreamViewClearsBackground(t *testing.T) {
	f := newFake()
	f.tel = sampleTelemetry()
	_, plain := newTester(t, f)
	if _, _, _, a := plain.Image().At(20, 880).RGBA(); a != 0xffff {
		t.Fatalf("well without a stream view: alpha %#x, want opaque", a)
	}

	app, _ := newTester(t, f)
	built := 0
	app.SetStreamView(func(c *mui.Context) { built++ })
	tt := mui.NewTester(app.View, 1440, 900)
	if built == 0 {
		t.Fatal("stream view not built")
	}
	if _, _, _, a := tt.Image().At(20, 880).RGBA(); a != 0 {
		t.Fatalf("stream view background: alpha %#x, want clear", a)
	}
	if _, ok := tt.Access(hudSummary(f.tel)); !ok {
		t.Fatal("HUD pill missing over the stream view")
	}

	f.Disconnect()
	tt.Frame()
	if _, _, _, a := tt.Image().At(20, 880).RGBA(); a != 0xffff {
		t.Fatalf("hosts screen after the stream: alpha %#x, want opaque", a)
	}
}
