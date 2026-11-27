package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"slices"
	"testing"

	mui "github.com/egoist/mygo/ui"

	"github.com/planckdotgg/planck/pkg/input"
	"github.com/planckdotgg/planck/pkg/inputtrace"
	"github.com/planckdotgg/planck/pkg/wire"
)

// useTrace swaps the client's tracer for one of the test's own, on, and
// returns a func that stops it and reads its records.
func useTrace(t *testing.T) func() []map[string]any {
	t.Helper()
	tr := inputtrace.New(inputtrace.SideClient)
	old := itrace
	itrace = tr
	t.Cleanup(func() {
		tr.Disable()
		itrace = old
	})
	path, err := tr.Enable(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return func() []map[string]any {
		t.Helper()
		if err := tr.Disable(); err != nil {
			t.Fatal(err)
		}
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		var out []map[string]any
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			var m map[string]any
			if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
				t.Fatalf("%q: %v", sc.Text(), err)
			}
			out = append(out, m)
		}
		return out
	}
}

func recsOf(recs []map[string]any, kind string) []map[string]any {
	return slices.DeleteFunc(slices.Clone(recs), func(r map[string]any) bool { return r["kind"] != kind })
}

// TestPointerLockTrace: the host's cursor states, each change of the hold
// with why it changed, the system calls under it, and relative motion all
// reach the trace.
func TestPointerLockTrace(t *testing.T) {
	read := useTrace(t)
	l, _, _ := newTestLock(t)
	l.setShape(wire.CursorShape{ID: 0xabc})
	l.setHost(wire.CursorState{Captured: true, HasPos: true, X: 100, Y: 200})
	l.frame(true, testPicture)
	l.motion(3.5, -2)
	l.motion(0.25, 0)
	l.setLocked(false)
	recs := read()

	hc := recsOf(recs, "host_cursor_state")
	if len(hc) != 1 || hc[0]["captured"] != true || hc[0]["x"] != float64(100) || hc[0]["shape_id"] != float64(0xabc) || hc[0]["seq"] != float64(1) {
		t.Fatalf("host_cursor_state %v", hc)
	}
	modes := recsOf(recs, "pointer_mode")
	if len(modes) != 2 {
		t.Fatalf("pointer_mode %v", modes)
	}
	in, out := modes[0], modes[1]
	if in["from"] != "free" || in["to"] != "relative" || in["reason"] != "host_state" || in["want"] != true || in["focused"] != true ||
		in["warp_target"] == nil || in["host_y"] != float64(200) {
		t.Fatalf("into relative %v", in)
	}
	if out["from"] != "relative" || out["to"] != "free" || out["reason"] != "user_release" || out["manual_release"] != true {
		t.Fatalf("out of relative %v", out)
	}
	var calls []string
	for _, r := range recsOf(recs, "pointer_sys") {
		calls = append(calls, r["call"].(string))
	}
	if !slices.Equal(calls, []string{"hold", "hide", "free", "show"}) {
		t.Fatalf("pointer_sys calls %v", calls)
	}
	hold := recsOf(recs, "pointer_sys")[0]
	if hold["mode"] != "relative" || hold["warped"] != true || hold["at"] == nil || hold["area"] == nil {
		t.Fatalf("hold %v", hold)
	}
	motion := recsOf(recs, "client_input")
	if len(motion) != 2 || motion[0]["sent_kind"] != "rel" || motion[0]["raw_dx"] != 3.5 || motion[0]["sent_dx"] != float64(3) ||
		motion[0]["sent_dy"] != float64(-2) || motion[1]["sent_kind"] != "suppressed" || motion[1]["suppress_reason"] != "under_one_unit" {
		t.Fatalf("motion %v", motion)
	}
}

// TestForwarderTrace: session_meta comes first, and again each time the
// trace turns on; the pointer is traced when it moves or what was sent
// changes, not every frame; buttons sent and dropped are traced.
func TestForwarderTrace(t *testing.T) {
	read := useTrace(t)
	f := &inputForwarder{send: func(input.Event) {}, video: func() (int, int, int, int) { return 1920, 1080, 1600, 800 },
		meta: func() windowMeta { return windowMeta{Platform: "test", Scale: 2, Fullscreen: true} }}
	tt := mui.NewTester(f.view, 800, 400)
	tt.Move(400, 225)
	tt.Frame()
	tt.Frame()
	tt.Frame()
	tt.Move(401, 225)
	tt.Frame()
	tt.Move(10, 225)
	tt.Frame()
	tt.PressButton(0, 10, 225)
	tt.Frame()
	tt.Move(400, 225)
	tt.Frame()
	tt.PressButton(0, 400, 225)
	tt.Frame()
	tt.ReleaseButton(0, 400, 225)
	tt.Frame()
	recs := read()

	// The view's bounds come a frame after it first builds, which is a
	// change of its own.
	metas := recsOf(recs, "session_meta")
	last := metas[len(metas)-1]
	if len(metas) > 2 || last["platform"] != "test" || last["scale"] != float64(2) || last["fullscreen"] != true ||
		last["video_w"] != float64(1920) || last["view"].(map[string]any)["w"] != float64(800) ||
		last["picture"].(map[string]any)["x"] != 44.5 {
		t.Fatalf("session_meta %v", metas)
	}
	if recs[1]["kind"] != "session_meta" {
		t.Fatalf("first record after trace_start is %v", recs[1]["kind"])
	}
	var ptr []string
	var buttons []string
	for _, r := range recsOf(recs, "client_input") {
		switch r["event"] {
		case "pointer":
			ptr = append(ptr, r["sent_kind"].(string)+":"+str(r["suppress_reason"]))
		case "button":
			buttons = append(buttons, r["sent_kind"].(string)+":"+str(r["suppress_reason"])+":"+str(r["pressed"]))
		}
	}
	if want := []string{"suppressed:no_view", "abs:", "abs:", "suppressed:off_picture", "abs:"}; !slices.Equal(ptr, want) {
		t.Fatalf("pointer records %v, want %v", ptr, want)
	}
	// The press dropped on the bar has its release dropped too.
	if want := []string{"suppressed:off_picture:true", "button::true", "button::false", "suppressed:not_held:false"}; !slices.Equal(buttons, want) {
		t.Fatalf("button records %v, want %v", buttons, want)
	}
	first := recsOf(recs, "client_input")[1]
	if first["raw_mygo_x"] != float64(400) || first["mode"] != "free" || first["sent_x"] == nil {
		t.Fatalf("first pointer %v", first)
	}
}

func str(v any) string {
	switch v := v.(type) {
	case nil:
		return ""
	case string:
		return v
	case bool:
		if v {
			return "true"
		}
		return "false"
	}
	return "?"
}

// TestSessionMetaOnEachEnable: a trace turned off and on again mid-stream
// starts with session_meta, with nothing else changed.
func TestSessionMetaOnEachEnable(t *testing.T) {
	tr := inputtrace.New(inputtrace.SideClient)
	old := itrace
	itrace = tr
	t.Cleanup(func() { tr.Disable(); itrace = old })
	_, tt, _ := barsForwarder(1920, 1080)
	dir := t.TempDir()
	count := func() int {
		path := tr.Path()
		tt.Frame()
		tt.Frame()
		if err := tr.Disable(); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		sc := bufio.NewScanner(bytes.NewReader(b))
		for sc.Scan() {
			var m map[string]any
			json.Unmarshal(sc.Bytes(), &m)
			if m["kind"] == "session_meta" {
				n++
			}
		}
		return n
	}
	tt.Frame()
	for i := range 2 {
		if _, err := tr.Enable(dir); err != nil {
			t.Fatal(err)
		}
		if n := count(); n != 1 {
			t.Fatalf("enable %d: %d session_meta records, want 1", i, n)
		}
	}
}

// TestTraceOffAllocatesNothing: with the trace off, the lock's hot paths
// build no records.
func TestTraceOffAllocatesNothing(t *testing.T) {
	if itrace.On() {
		t.Fatal("the process tracer is on")
	}
	sys := &fakePointer{t: t}
	sent := 0
	l := newPointerLock(sys, func(input.Event) { sent++ })
	l.frame(true, testPicture)
	l.setHost(wire.CursorState{Captured: true})
	l.frame(true, testPicture)
	if a := testing.AllocsPerRun(500, func() { l.motion(1, 1) }); a != 0 {
		t.Fatalf("motion allocates %.1f with the trace off", a)
	}
	if a := testing.AllocsPerRun(500, func() { l.setHost(wire.CursorState{Captured: true, X: 7}) }); a != 0 {
		t.Fatalf("setHost allocates %.1f with the trace off", a)
	}
}
