package session

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/planckdotgg/planck/pkg/capture/cursor"
	"github.com/planckdotgg/planck/pkg/input"
	"github.com/planckdotgg/planck/pkg/inputtrace"
	"github.com/planckdotgg/planck/pkg/transport"
	"github.com/planckdotgg/planck/pkg/wire"
)

func traceRecords(t *testing.T, path string) []map[string]any {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []map[string]any
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("%q: %v", sc.Text(), err)
		}
		out = append(out, m)
	}
	return out
}

func ofKind(recs []map[string]any, kind string) []map[string]any {
	var out []map[string]any
	for _, r := range recs {
		if r["kind"] == kind {
			out = append(out, r)
		}
	}
	return out
}

// privateTrace gives the host a tracer of its own.
func privateTrace(tr *inputtrace.Tracer) func(*Host) {
	return func(h *Host) { h.itrace.t = tr }
}

// TestInputTraceFollowsClient: the host traces while the client asks,
// from the start of a session or from the middle of one, and stops when
// it asks, when its stream ends, and when a client that does not trace
// takes over.
func TestInputTraceFollowsClient(t *testing.T) {
	tr := inputtrace.New(inputtrace.SideHost)
	hs := startHostWith(t, t.TempDir(), "127.0.0.1:0", nil, "off", privateTrace(tr))
	addr := hs.host.Addr().String()
	r := newClient(t, t.TempDir(), hs.host.PIN())
	if err := connect(t, r, addr); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "video", func() bool { return r.sink.Units.Load() >= 10 })
	if tr.On() {
		t.Fatal("host traces with no client asking")
	}

	r.client.SetInputTrace(true)
	waitFor(t, "the host to trace mid-stream", tr.On)
	path := tr.Path()
	r.client.SetInputTrace(false)
	waitFor(t, "the host to stop", func() bool { return !tr.On() })
	recs := traceRecords(t, path)
	if len(ofKind(recs, "host_meta")) == 0 || len(ofKind(recs, "trace_control")) == 0 {
		t.Fatalf("trace turned on mid-stream has no host_meta or trace_control: %v", recs)
	}
	meta := ofKind(recs, "host_meta")[0]
	if meta["side"] != "host" || meta["source"] != "fake" || meta["platform"] == "" {
		t.Fatalf("host_meta %v", meta)
	}

	r.client.SetInputTrace(true)
	waitFor(t, "the host to trace again", tr.On)
	r.client.Disconnect()
	waitFor(t, "the host to stop as the stream ends", func() bool { return !tr.On() })

	tracing := newClient(t, r.client.dir, "")
	tracing.client.SetInputTrace(true)
	if err := connect(t, tracing, addr); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the host to trace from the connect", tr.On)
	path = tr.Path()

	quiet := newClient(t, r.client.dir, "")
	if err := connect(t, quiet, addr); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the host to stop for a client that does not trace", func() bool { return !tr.On() })
	waitFor(t, "video for the quiet client", func() bool { return quiet.sink.Units.Load() >= 10 })
	if tr.On() {
		t.Fatal("the host traces for the quiet client")
	}
	if len(ofKind(traceRecords(t, path), "host_meta")) == 0 {
		t.Fatal("trace begun at connect has no host_meta")
	}
}

// TestInputTraceForcedByHost: a host started with InputTrace traces from
// Start to Stop, whatever the client asks.
func TestInputTraceForcedByHost(t *testing.T) {
	tr := inputtrace.New(inputtrace.SideHost)
	hs := startHostWith(t, t.TempDir(), "127.0.0.1:0", nil, "off", func(h *Host) {
		h.itrace.t = tr
		h.cfg.InputTrace = true
	})
	if !tr.On() {
		t.Fatal("forced host does not trace after Start")
	}
	r := newClient(t, t.TempDir(), hs.host.PIN())
	r.client.SetInputTrace(true)
	if err := connect(t, r, hs.host.Addr().String()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "video", func() bool { return r.sink.Units.Load() >= 10 })
	r.client.SetInputTrace(false)
	time.Sleep(200 * time.Millisecond)
	if !tr.On() {
		t.Fatal("the client's stop ended a forced trace")
	}
	r.client.Disconnect()
	waitFor(t, "the host to see the goodbye", func() bool { return !hs.host.Stats().Connected })
	if !tr.On() {
		t.Fatal("the stream's end ended a forced trace")
	}
	hs.host.Stop()
	if tr.On() {
		t.Fatal("still tracing after Stop")
	}
}

// TestInputTraceOlderHost: a host without FeatureInputTrace, which ends a
// session on a message it does not know, never hears InputTrace.
func TestInputTraceOlderHost(t *testing.T) {
	tr := inputtrace.New(inputtrace.SideHost)
	hs := startHostWith(t, t.TempDir(), "127.0.0.1:0", nil, "off", func(h *Host) {
		h.itrace.t = tr
		listen := h.listen
		h.listen = func(addr string, c transport.ServerConfig) (*transport.Server, error) {
			c.InputTrace = false
			return listen(addr, c)
		}
	})
	r := newClient(t, t.TempDir(), hs.host.PIN())
	r.client.SetInputTrace(true)
	if err := connect(t, r, hs.host.Addr().String()); err != nil {
		t.Fatal(err)
	}
	r.client.SetInputTrace(false)
	r.client.SetInputTrace(true)
	n := r.sink.Units.Load()
	waitFor(t, "video to keep coming", func() bool { return r.sink.Units.Load() >= n+30 })
	if !r.client.Stats().Connected {
		t.Fatal("session ended")
	}
	if tr.On() {
		t.Fatal("a host without the feature was asked to trace")
	}
}

// fakeCursorInjector moves a pretend cursor as SendInput would, and
// reports what it posts as the Windows injector does.
type fakeCursorInjector struct {
	mu   sync.Mutex
	x, y int
	on   func() bool
	sent func(input.SentMouse)
}

func (f *fakeCursorInjector) Inject(ev input.Event) error {
	f.mu.Lock()
	var m input.SentMouse
	switch ev.Type {
	case input.TypeMouseMoveRel:
		f.x += int(ev.MouseRel.DX)
		f.y += int(ev.MouseRel.DY)
		m = input.SentMouse{Flags: 1, DX: int32(ev.MouseRel.DX), DY: int32(ev.MouseRel.DY)}
	case input.TypeMouseMoveAbs:
		f.x, f.y = int(ev.MouseAbs.X)/32, int(ev.MouseAbs.Y)/32
		m = input.SentMouse{Flags: 0x8001, DX: int32(ev.MouseAbs.X), DY: int32(ev.MouseAbs.Y)}
	case input.TypeMouseButton:
		m = input.SentMouse{Flags: 2}
	default:
		f.mu.Unlock()
		return nil
	}
	f.mu.Unlock()
	if f.sent != nil && f.on() {
		f.sent(m)
	}
	return nil
}

func (f *fakeCursorInjector) Close() error { return nil }
func (f *fakeCursorInjector) TraceSends(on func() bool, s func(input.SentMouse)) {
	f.on, f.sent = on, s
}

func (f *fakeCursorInjector) read() (cursor.State, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return cursor.State{Visible: true, X: f.x, Y: f.y, Clip: cursor.Rect{W: 1920, H: 1080}, Desktop: cursor.Rect{W: 1920, H: 1080}}, nil
}

func (f *fakeCursorInjector) warp(x, y int) {
	f.mu.Lock()
	f.x, f.y = x, y
	f.mu.Unlock()
}

func tracedHost(t *testing.T) (*Host, *inputtrace.Tracer, *fakeCursorInjector) {
	t.Helper()
	inj := &fakeCursorInjector{x: 100, y: 100}
	h, err := NewHost(HostConfig{ConfigDir: t.TempDir(), Name: "trace-host", ListenAddr: "127.0.0.1:0", InjectInput: true,
		Injector: inj, ReadCursor: inj.read, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	tr := inputtrace.New(inputtrace.SideHost)
	h.itrace.t = tr
	h.openInput()
	if inj.sent == nil {
		t.Fatal("the host did not hook the injector's sends")
	}
	return h, tr, inj
}

func sendTo(t *testing.T, h *Host, seq uint32, ev input.Event) {
	t.Helper()
	in, err := toWire(ev)
	if err != nil {
		t.Fatal(err)
	}
	in.Seq = seq
	h.onInput(nil, in)
}

// TestHostInputTrace: each mouse input is traced with what the injector
// posted and the cursor before and after; a cursor moved between two
// injections with nothing of the host's to move it is a jump. Off, the
// host traces nothing.
func TestHostInputTrace(t *testing.T) {
	h, tr, inj := tracedHost(t)
	sendTo(t, h, 1, input.Event{Type: input.TypeMouseMoveRel, MouseRel: input.MouseMoveRel{DX: 5, DY: -3}})
	h.askTrace(true)
	path := tr.Path()
	sendTo(t, h, 2, input.Event{Type: input.TypeMouseMoveRel, MouseRel: input.MouseMoveRel{DX: 5, DY: -3}})
	sendTo(t, h, 3, input.Event{Type: input.TypeKey, Key: input.KeyEvent{Usage: 0x04, Pressed: true}})
	inj.warp(960, 540)
	sendTo(t, h, 4, input.Event{Type: input.TypeMouseMoveRel, MouseRel: input.MouseMoveRel{DX: 1, DY: 1}})
	sendTo(t, h, 5, input.Event{Type: input.TypeMouseButton, Button: input.MouseButton{Button: input.ButtonLeft, Pressed: true}})
	h.askTrace(false)

	recs := traceRecords(t, path)
	ins := ofKind(recs, "host_input")
	var seqs []float64
	for _, r := range ins {
		seqs = append(seqs, r["seq"].(float64))
	}
	if !slices.Equal(seqs, []float64{2, 4, 5}) {
		t.Fatalf("host_input seqs %v, want 2 4 5 (mouse only, traced while on)", seqs)
	}
	first := ins[0]
	before, after := first["before"].(map[string]any), first["after"].(map[string]any)
	sent := first["sent"].([]any)[0].(map[string]any)
	if first["type"] != "mouse_rel" || first["dx"] != float64(5) || first["dy"] != float64(-3) || first["injected"] != true ||
		before["x"] != float64(105) || before["y"] != float64(97) || after["x"] != float64(110) || after["y"] != float64(94) ||
		sent["flags"] != float64(1) || sent["dx"] != float64(5) || sent["dy"] != float64(-3) {
		t.Fatalf("host_input %v", first)
	}
	if b := ins[2]; b["type"] != "button" || b["button"] != "left" || b["pressed"] != true {
		t.Fatalf("button %v", b)
	}
	jumps := ofKind(recs, "cursor_jump")
	if len(jumps) != 1 {
		t.Fatalf("jumps %v, want the one warp", jumps)
	}
	j := jumps[0]
	if j["source"] != "input_gap" || j["to"].(map[string]any)["x"] != float64(960) || j["from"].(map[string]any)["x"] != float64(110) {
		t.Fatalf("jump %v", j)
	}
}

// TestCursorPollTrace: a poll is traced when the cursor or the report
// changed, with the report's action; a cursor that moved with no
// injection since the last poll is a jump, and one an injection moved is
// not.
func TestCursorPollTrace(t *testing.T) {
	h, tr, inj := tracedHost(t)
	h.askTrace(true)
	path := tr.Path()
	var ct cursorTrace
	var report cursorReport
	poll := func(s cursor.State) {
		m := cursorMessage(s, cursor.Rect{W: 1920, H: 1080})
		act := report.decide(m)
		if act == reportEmit {
			report.sent(m)
		}
		ct.poll(h, s, m, false, act)
	}
	shown := cursor.State{Visible: true, X: 100, Y: 100, Handle: 7}
	hidden := shown
	hidden.Visible, hidden.Handle = false, 0
	poll(shown)
	poll(shown)
	poll(hidden)
	poll(shown)
	inj.warp(100, 100)
	sendTo(t, h, 1, input.Event{Type: input.TypeMouseMoveRel, MouseRel: input.MouseMoveRel{DX: 40}})
	moved := shown
	moved.X = 140
	poll(moved)
	jumped := moved
	jumped.X, jumped.Y = 960, 540
	poll(jumped)
	h.askTrace(false)

	recs := traceRecords(t, path)
	var actions []string
	for _, r := range ofKind(recs, "cursor_poll") {
		actions = append(actions, r["action"].(string))
	}
	if want := []string{"emit", "hold_hide", "drop_flicker", "same", "same"}; !slices.Equal(actions, want) {
		t.Fatalf("cursor_poll actions %v, want %v", actions, want)
	}
	first := ofKind(recs, "cursor_poll")[0]
	if first["hcursor"] != float64(7) || first["sent_has_pos"] != true || first["clip_rect"] == nil {
		t.Fatalf("cursor_poll %v", first)
	}
	var polls []map[string]any
	for _, j := range ofKind(recs, "cursor_jump") {
		if j["source"] == "poll" {
			polls = append(polls, j)
		}
	}
	if len(polls) != 1 || polls[0]["dx"] != float64(820) || polls[0]["dy"] != float64(440) {
		t.Fatalf("poll jumps %v, want the one to 960, 540", polls)
	}
}

// TestCursorReportActions: what the report made of each poll, as the
// trace names it.
func TestCursorReportActions(t *testing.T) {
	shown := wire.CursorState{Visible: true}
	hidden := wire.CursorState{}
	var r cursorReport
	var got []reportAction
	for _, m := range []wire.CursorState{shown, shown, hidden, shown, hidden, hidden} {
		a := r.decide(m)
		if a == reportEmit {
			r.sent(m)
		}
		got = append(got, a)
	}
	want := []reportAction{reportEmit, reportSame, reportHoldHide, reportDropFlicker, reportHoldHide, reportEmit}
	if !slices.Equal(got, want) {
		t.Fatalf("actions %v, want %v", got, want)
	}
}

// TestInputTraceToggledDuringConnect: a toggle after the connect asked the
// host but before the stream is live reaches the host once it is.
func TestInputTraceToggledDuringConnect(t *testing.T) {
	for _, final := range []bool{true, false} {
		t.Run(fmt.Sprintf("to %v", final), func(t *testing.T) {
			tr := inputtrace.New(inputtrace.SideHost)
			hs := startHostWith(t, t.TempDir(), "127.0.0.1:0", nil, "off", privateTrace(tr))
			r := newClient(t, t.TempDir(), hs.host.PIN())
			r.client.SetInputTrace(!final)
			r.client.beforeLive = func(*conn, *route) { r.client.SetInputTrace(final) }
			if err := connect(t, r, hs.host.Addr().String()); err != nil {
				t.Fatal(err)
			}
			waitFor(t, "video", func() bool { return r.sink.Units.Load() >= 10 })
			waitFor(t, "the host to follow the last toggle", func() bool { return tr.On() == final })
			time.Sleep(200 * time.Millisecond)
			if tr.On() != final {
				t.Fatalf("host traces %v, client asked %v", tr.On(), final)
			}
		})
	}
}

// TestInputTraceEndsWithFailedStart: a forced trace that Start opened
// closes when Start fails.
func TestInputTraceEndsWithFailedStart(t *testing.T) {
	tr := inputtrace.New(inputtrace.SideHost)
	h, err := NewHost(HostConfig{
		ConfigDir: t.TempDir(), Name: "test-host", ListenAddr: "127.0.0.1:0", DiscoveryAddr: "off", NoTailscale: true,
		OpenVideo:  func(VideoParams) (VideoPipe, error) { return &fakePipe{}, nil },
		InputTrace: true, Logf: t.Logf,
	})
	if err != nil {
		t.Fatal(err)
	}
	h.itrace.t = tr
	bindErr := errors.New("bind: address in use")
	h.listen = func(string, transport.ServerConfig) (*transport.Server, error) {
		if !tr.On() {
			t.Error("the trace was not on as Start listened")
		}
		return nil, bindErr
	}
	if err := h.Start(context.Background()); !errors.Is(err, bindErr) {
		t.Fatalf("Start: %v", err)
	}
	if tr.On() || tr.Path() != "" {
		t.Fatalf("trace still open after a failed Start: %s", tr.Path())
	}
}

// TestRetraceForgetsTheCursor: a cursor moved while the trace was off is
// no jump in the next trace, from the next input or the next poll; one
// moved during it still is.
func TestRetraceForgetsTheCursor(t *testing.T) {
	h, tr, inj := tracedHost(t)
	rel := input.Event{Type: input.TypeMouseMoveRel, MouseRel: input.MouseMoveRel{DX: 1, DY: 1}}
	var ct cursorTrace
	poll := func() {
		s, _ := inj.read()
		ct.poll(h, s, cursorMessage(s, cursor.Rect{W: 1920, H: 1080}), false, reportSame)
	}
	h.askTrace(true)
	sendTo(t, h, 1, rel)
	poll()
	h.askTrace(false)
	inj.warp(960, 540)
	sendTo(t, h, 2, rel)
	h.askTrace(true)
	path := tr.Path()
	poll()
	sendTo(t, h, 3, rel)
	inj.warp(200, 200)
	sendTo(t, h, 4, rel)
	h.askTrace(false)

	jumps := ofKind(traceRecords(t, path), "cursor_jump")
	if len(jumps) != 1 || jumps[0]["source"] != "input_gap" || jumps[0]["to"].(map[string]any)["x"] != float64(200) {
		t.Fatalf("jumps %v, want only the warp to 200, 200 while tracing", jumps)
	}
}

// TestRetraceDuringAnInput: an input that read the cursor before the trace
// went off and on again is no base for the next trace, though it ends in
// it.
func TestRetraceDuringAnInput(t *testing.T) {
	h, tr, inj := tracedHost(t)
	rel := input.Event{Type: input.TypeMouseMoveRel, MouseRel: input.MouseMoveRel{DX: 1, DY: 1}}
	var ct cursorTrace
	poll := func() {
		s, _ := inj.read()
		ct.poll(h, s, cursorMessage(s, cursor.Rect{W: 1920, H: 1080}), false, reportSame)
	}
	h.askTrace(true)
	sendTo(t, h, 1, rel)

	var path string
	reads := 0
	h.cfg.ReadCursor = func() (cursor.State, error) {
		s, err := inj.read()
		if reads++; reads == 2 {
			h.askTrace(false)
			inj.warp(960, 540)
			h.askTrace(true)
			path = tr.Path()
			poll()
		}
		return s, err
	}
	sendTo(t, h, 2, rel)
	h.cfg.ReadCursor = inj.read
	poll()
	sendTo(t, h, 3, rel)
	inj.warp(200, 200)
	sendTo(t, h, 4, rel)
	h.askTrace(false)

	jumps := ofKind(traceRecords(t, path), "cursor_jump")
	if len(jumps) != 1 || jumps[0]["source"] != "input_gap" || jumps[0]["to"].(map[string]any)["x"] != float64(200) {
		t.Fatalf("jumps %v, want only the warp to 200, 200 while tracing", jumps)
	}
}
