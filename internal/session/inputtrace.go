package session

import (
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/planckdotgg/planck/pkg/capture/cursor"
	"github.com/planckdotgg/planck/pkg/input"
	"github.com/planckdotgg/planck/pkg/inputtrace"
	"github.com/planckdotgg/planck/pkg/wire"
)

// jumpPixels is how far the cursor moves, on either axis, with no input of
// the host's to move it, before the trace calls it a jump: a game's
// SetCursorPos, or the host's own mouse.
const jumpPixels = 2

// hostTrace is the host's input trace (pkg/inputtrace): on from Start
// with HostConfig.InputTrace, and while the streaming client traces its
// own.
type hostTrace struct {
	t *inputtrace.Tracer

	mu     sync.Mutex
	forced bool
	asked  bool

	// call keeps one traced injection at a time; sent are the mouse events
	// the injector posted during it, and prev the cursor after the last.
	call    sync.Mutex
	sentMu  sync.Mutex
	sent    []input.SentMouse
	prev    tracePoint
	prevSet bool
	// injects counts the traced injections and after holds the cursor
	// after the last, x in the high half, for cursorLoop's jump check.
	injects atomic.Uint32
	after   atomic.Uint64
}

// on reports whether the host traces.
func (ht *hostTrace) on() bool { return ht.t != nil && ht.t.On() }

type tracePoint struct {
	X int `json:"x"`
	Y int `json:"y"`
}

type traceRect struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

func rectOfInput(r input.Rect) traceRect   { return traceRect{r.X, r.Y, r.W, r.H} }
func rectOfCursor(r cursor.Rect) traceRect { return traceRect{r.X, r.Y, r.W, r.H} }

func packPoint(x, y int) uint64       { return uint64(uint32(int32(x)))<<32 | uint64(uint32(int32(y))) }
func unpackPoint(p uint64) (int, int) { return int(int32(uint32(p >> 32))), int(int32(uint32(p))) }

// startTrace turns the trace on for good with HostConfig.InputTrace.
func (h *Host) startTrace() {
	h.itrace.mu.Lock()
	defer h.itrace.mu.Unlock()
	h.itrace.forced = h.cfg.InputTrace
	h.applyTraceLocked(false)
}

// stopTrace ends the trace as the host stops.
func (h *Host) stopTrace() {
	h.itrace.mu.Lock()
	defer h.itrace.mu.Unlock()
	h.itrace.forced, h.itrace.asked = false, false
	h.applyTraceLocked(false)
}

// askTrace takes the client's wire.InputTrace, and the end of its stream
// as a request to stop.
func (h *Host) askTrace(on bool) {
	h.itrace.mu.Lock()
	defer h.itrace.mu.Unlock()
	was := h.itrace.asked
	h.itrace.asked = on
	h.applyTraceLocked(on && !was)
}

type traceControl struct {
	Forced bool `json:"forced"`
	Asked  bool `json:"client_asked"`
}

// applyTraceLocked turns the trace on or off as forced and asked want,
// writing host_meta as it turns on, or with meta. Called with mu held.
func (h *Host) applyTraceLocked(meta bool) {
	ht := &h.itrace
	if ht.t == nil {
		return
	}
	want := ht.forced || ht.asked
	switch {
	case want && !ht.t.On():
		path, err := ht.t.Enable(inputtrace.Dir(h.dir))
		if err != nil {
			h.logf("planck: input trace: %v", err)
			return
		}
		h.logf("planck: input trace in %s", path)
		meta = true
	case !want && ht.t.On():
		if err := ht.t.Disable(); err != nil {
			h.logf("planck: input trace: %v", err)
		}
		return
	case !want:
		return
	}
	inputtrace.Emit(ht.t, "trace_control", traceControl{Forced: ht.forced, Asked: ht.asked})
	if st := h.current(); meta && st != nil {
		st.traceMeta()
	}
}

type hostMeta struct {
	Platform      string      `json:"platform"`
	VideoW        int         `json:"video_w"`
	VideoH        int         `json:"video_h"`
	FPS           int         `json:"fps"`
	Source        string      `json:"source"`
	Path          string      `json:"path"`
	Display       traceRect   `json:"capture_display"`
	Desktop       traceRect   `json:"input_desktop"`
	VirtualScreen traceRect   `json:"virtual_screen"`
	Clip          traceRect   `json:"clip_rect"`
	Cursor        *tracePoint `json:"cursor,omitempty"`
	DPI           int         `json:"dpi"`
	CursorErr     string      `json:"cursor_err,omitempty"`
}

// traceMeta writes what the host's coordinates mean: the display captured
// and the desktop absolute moves cover, with the system's own view.
func (st *stream) traceMeta() {
	t := st.h.itrace.t
	if !st.h.itrace.on() {
		return
	}
	st.mu.Lock()
	info := st.info
	st.mu.Unlock()
	m := hostMeta{Platform: runtime.GOOS + "/" + runtime.GOARCH, VideoW: info.Width, VideoH: info.Height, FPS: info.FPS,
		Source: info.Source, Path: st.path.String(), Display: rectOfInput(info.Display), Desktop: rectOfInput(info.Desktop)}
	if s, err := st.h.readCursor(); err == nil {
		m.VirtualScreen, m.Clip, m.Cursor = rectOfCursor(s.Desktop), rectOfCursor(s.Clip), &tracePoint{s.X, s.Y}
		m.DPI = cursor.DPIAt(s.X, s.Y)
	} else {
		m.CursorErr = err.Error()
	}
	inputtrace.Emit(t, "host_meta", m)
}

func (h *Host) readCursor() (cursor.State, error) {
	if h.cfg.ReadCursor != nil {
		return h.cfg.ReadCursor()
	}
	return cursor.Read()
}

// traceSent is the injector's input.SendTracer.
func (h *Host) traceSent(m input.SentMouse) {
	if !h.itrace.on() {
		return
	}
	h.itrace.sentMu.Lock()
	h.itrace.sent = append(h.itrace.sent, m)
	h.itrace.sentMu.Unlock()
}

// hostInput is one mouse input from the client as the host took it: what
// came, what the injector posted, and where the cursor was before and
// after.
type hostInput struct {
	Seq      uint32  `json:"seq"`
	Type     string  `json:"type"`
	X        *uint16 `json:"x,omitempty"`
	Y        *uint16 `json:"y,omitempty"`
	DX       int16   `json:"dx,omitempty"`
	DY       int16   `json:"dy,omitempty"`
	Button   string  `json:"button,omitempty"`
	Pressed  bool    `json:"pressed,omitempty"`
	Injected bool    `json:"injected"`
	Err      string  `json:"err,omitempty"`
	// Sent are the system calls made: SendInput's flags, dx, dy and data.
	Sent   []input.SentMouse `json:"sent,omitempty"`
	Before *tracePoint       `json:"before,omitempty"`
	After  *tracePoint       `json:"after,omitempty"`
}

// cursorJump is the cursor found moved with nothing of the host's to
// move it: between two injections (source "input_gap"), or between two
// polls with no injection since (source "poll").
type cursorJump struct {
	Source string     `json:"source"`
	From   tracePoint `json:"from"`
	To     tracePoint `json:"to"`
	DX     int        `json:"dx"`
	DY     int        `json:"dy"`
}

func moved(a, b tracePoint) bool {
	return abs(a.X-b.X) > jumpPixels || abs(a.Y-b.Y) > jumpPixels
}

func buttonName(b input.Button) string {
	switch b {
	case input.ButtonLeft:
		return "left"
	case input.ButtonRight:
		return "right"
	case input.ButtonMiddle:
		return "middle"
	case input.ButtonX1:
		return "x1"
	case input.ButtonX2:
		return "x2"
	}
	return "unknown"
}

// traceInputStart begins the trace of ev, a mouse event, before it is
// injected, or returns nil for another.
func (h *Host) traceInputStart(in wire.Input, ev input.Event) *hostInput {
	rec := hostInput{Seq: in.Seq}
	switch ev.Type {
	case input.TypeMouseMoveAbs:
		x, y := ev.MouseAbs.X, ev.MouseAbs.Y
		rec.Type, rec.X, rec.Y = "mouse_abs", &x, &y
	case input.TypeMouseMoveRel:
		rec.Type, rec.DX, rec.DY = "mouse_rel", ev.MouseRel.DX, ev.MouseRel.DY
	case input.TypeMouseButton:
		rec.Type, rec.Button, rec.Pressed = "button", buttonName(ev.Button.Button), ev.Button.Pressed
	case input.TypeMouseWheel:
		rec.Type, rec.DX, rec.DY = "wheel", ev.Wheel.DX, ev.Wheel.DY
	default:
		return nil
	}
	ht := &h.itrace
	ht.call.Lock()
	ht.sentMu.Lock()
	ht.sent = ht.sent[:0]
	ht.sentMu.Unlock()
	if s, err := h.readCursor(); err == nil {
		rec.Before = &tracePoint{s.X, s.Y}
	}
	return &rec
}

// traceInputEnd writes the trace begun by traceInputStart, and a jump
// where the cursor moved since the last injection with nothing of the
// host's to move it.
func (h *Host) traceInputEnd(rec *hostInput, injected bool, err error) {
	ht := &h.itrace
	defer ht.call.Unlock()
	rec.Injected = injected
	if err != nil {
		rec.Err = err.Error()
	}
	ht.sentMu.Lock()
	rec.Sent = append([]input.SentMouse(nil), ht.sent...)
	ht.sentMu.Unlock()
	if s, err := h.readCursor(); err == nil {
		rec.After = &tracePoint{s.X, s.Y}
		ht.after.Store(packPoint(s.X, s.Y))
	}
	if rec.Before != nil && ht.prevSet && moved(ht.prev, *rec.Before) {
		inputtrace.Emit(ht.t, "cursor_jump", cursorJump{Source: "input_gap", From: ht.prev, To: *rec.Before,
			DX: rec.Before.X - ht.prev.X, DY: rec.Before.Y - ht.prev.Y})
	}
	ht.prevSet = rec.After != nil
	if rec.After != nil {
		ht.prev = *rec.After
	}
	ht.injects.Add(1)
	inputtrace.Emit(ht.t, "host_input", *rec)
}

// cursorTrace follows cursorLoop's polls for the trace.
type cursorTrace struct {
	epoch   uint32
	started bool
	last    cursor.State
	injects uint32
	base    tracePoint
	based   bool
}

type cursorPollRec struct {
	Visible        bool      `json:"visible"`
	Captured       bool      `json:"captured"`
	CursorX        int       `json:"cursor_x"`
	CursorY        int       `json:"cursor_y"`
	HCursor        uint64    `json:"hcursor"`
	Clip           traceRect `json:"clip_rect"`
	Desktop        traceRect `json:"virtual_screen"`
	SentVisible    bool      `json:"sent_visible"`
	SentCaptured   bool      `json:"sent_captured"`
	SentX          uint16    `json:"sent_x"`
	SentY          uint16    `json:"sent_y"`
	SentHasPos     bool      `json:"sent_has_pos"`
	WarpCaptured   bool      `json:"warp_captured"`
	Action         string    `json:"action"`
	ForegroundHWND uint64    `json:"foreground_hwnd,omitempty"`
	ForegroundPID  uint32    `json:"foreground_pid,omitempty"`
}

var reportActionNames = [...]string{reportSame: "same", reportEmit: "emit", reportHoldHide: "hold_hide",
	reportDropFlicker: "drop_flicker", reportSendFailed: "emit_failed"}

// poll traces a poll of the cursor, s as the system reported it and m as
// the client hears of it, when it changed or the report did something;
// and a jump where the cursor moved with no injection since the last poll.
func (ct *cursorTrace) poll(h *Host, s cursor.State, m wire.CursorState, warped bool, act reportAction) {
	t := h.itrace.t
	if e := t.Epoch(); e != ct.epoch {
		*ct = cursorTrace{epoch: e}
	}
	if !ct.started || s != ct.last || act != reportSame {
		rec := cursorPollRec{Visible: s.Visible, Captured: s.Captured, CursorX: s.X, CursorY: s.Y, HCursor: uint64(s.Handle),
			Clip: rectOfCursor(s.Clip), Desktop: rectOfCursor(s.Desktop),
			SentVisible: m.Visible, SentCaptured: m.Captured, SentX: m.X, SentY: m.Y, SentHasPos: m.HasPos, WarpCaptured: warped,
			Action: reportActionNames[act]}
		hwnd, pid := cursor.Foreground()
		rec.ForegroundHWND, rec.ForegroundPID = uint64(hwnd), pid
		inputtrace.Emit(t, "cursor_poll", rec)
	}
	if n := h.itrace.injects.Load(); n != ct.injects {
		ct.injects = n
		x, y := unpackPoint(h.itrace.after.Load())
		ct.base, ct.based = tracePoint{x, y}, true
	}
	at := tracePoint{s.X, s.Y}
	if ct.based && moved(ct.base, at) {
		inputtrace.Emit(t, "cursor_jump", cursorJump{Source: "poll", From: ct.base, To: at, DX: at.X - ct.base.X, DY: at.Y - ct.base.Y})
	}
	ct.base, ct.based = at, true
	ct.started, ct.last = true, s
}
