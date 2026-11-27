package main

import (
	"runtime"

	"github.com/planckdotgg/planck/pkg/input"
	"github.com/planckdotgg/planck/pkg/inputtrace"
)

// itrace is the client's input trace (pkg/inputtrace); tests swap it for
// one of their own.
var itrace = inputtrace.Client

var pointerModeNames = [...]string{pointerFree: "free", pointerRelative: "relative", pointerConfined: "confined"}

func (m pointerMode) String() string {
	if int(m) < len(pointerModeNames) {
		return pointerModeNames[m]
	}
	return "unknown"
}

type tracePt struct {
	X float32 `json:"x"`
	Y float32 `json:"y"`
}

type traceArea struct {
	X float32 `json:"x"`
	Y float32 `json:"y"`
	W float32 `json:"w"`
	H float32 `json:"h"`
}

type traceBox struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

func areaOf(r rect) traceArea { return traceArea{r.X, r.Y, r.W, r.H} }

func spotOf(s spot) *tracePt {
	if !s.set {
		return nil
	}
	return &tracePt{s.x, s.y}
}

// windowMeta is the stream window as the trace's session_meta tells of it:
// what the client's coordinates mean.
type windowMeta struct {
	Platform string `json:"platform"`
	// Window is the window's frame and Content its content, in the
	// screen's points or DIPs; Pixels the content in pixels and Scale
	// pixels per DIP.
	Window     traceBox `json:"window_bounds"`
	Content    traceBox `json:"content_bounds"`
	PixelW     int      `json:"content_px_w"`
	PixelH     int      `json:"content_px_h"`
	Scale      float64  `json:"scale"`
	Display    traceBox `json:"display_bounds"`
	DisplayScl float64  `json:"display_scale"`
	Fullscreen bool     `json:"fullscreen"`
}

// sessionMeta is the context of the client's records: the window, the
// stream view, and the picture's place in it.
type sessionMeta struct {
	windowMeta
	VideoW   int       `json:"video_w"`
	VideoH   int       `json:"video_h"`
	DrawW    int       `json:"drawable_w"`
	DrawH    int       `json:"drawable_h"`
	View     traceArea `json:"view"`
	Picture  traceArea `json:"picture"`
	Focused  bool      `json:"focused"`
	Relative bool      `json:"relative"`
}

func platformName() string { return runtime.GOOS + "/" + runtime.GOARCH }

type hostCursorRec struct {
	Seq      uint32 `json:"seq"`
	Visible  bool   `json:"visible"`
	Captured bool   `json:"captured"`
	HasPos   bool   `json:"has_pos"`
	X        uint16 `json:"x"`
	Y        uint16 `json:"y"`
	// ShapeID is the host's cursor image last received, 0 for none.
	ShapeID uint64 `json:"shape_id"`
}

type pointerModeRec struct {
	From   string    `json:"from"`
	To     string    `json:"to"`
	Reason string    `json:"reason"`
	Want   bool      `json:"want"`
	Area   traceArea `json:"area"`
	// HostX and HostY are the host's cursor, 0..65535 across the picture,
	// as the lock last heard of it.
	HostX       uint16   `json:"host_x"`
	HostY       uint16   `json:"host_y"`
	HostHasPos  bool     `json:"host_has_pos"`
	HostVisible bool     `json:"host_visible"`
	HostCapt    bool     `json:"host_captured"`
	WarpTarget  *tracePt `json:"warp_target,omitempty"`
	WarpConfirm bool     `json:"warp_confirmed"`
	WarpSeq     uint32   `json:"warp_seq"`
	FreshHost   bool     `json:"fresh_host_state"`
	Manual      bool     `json:"manual_lock"`
	ManualRel   bool     `json:"manual_release"`
	Focused     bool     `json:"focused"`
	MenuOpen    bool     `json:"menu_open"`
}

// pointerSysRec is one call into the system's pointer: hold and free as
// the lock makes them, and the platform's own calls under them.
type pointerSysRec struct {
	Call string     `json:"call"`
	Mode string     `json:"mode,omitempty"`
	Area *traceArea `json:"area,omitempty"`
	At   *tracePt   `json:"at,omitempty"`
	// Screen is a place or rectangle in the screen's coordinates, as the
	// platform call took it.
	ScreenX *float64  `json:"screen_x,omitempty"`
	ScreenY *float64  `json:"screen_y,omitempty"`
	Rect    *traceBox `json:"rect,omitempty"`
	Warped  *bool     `json:"warped,omitempty"`
	Ret     int64     `json:"ret"`
	Err     string    `json:"err,omitempty"`
}

func traceSys(r pointerSysRec) { inputtrace.Emit(itrace, "pointer_sys", r) }

func f64p(v float64) *float64 { return &v }

// clientInputRec is the pointer, a button or the wheel as the forwarder
// took it, and what it sent the host.
type clientInputRec struct {
	Event string `json:"event"`
	// RawX and RawY are where MyGo has the pointer in the view, RawDX and
	// RawDY the system's relative motion.
	RawX  *float32 `json:"raw_mygo_x,omitempty"`
	RawY  *float32 `json:"raw_mygo_y,omitempty"`
	RawOn *bool    `json:"raw_mygo_over,omitempty"`
	RawDX float64  `json:"raw_dx,omitempty"`
	RawDY float64  `json:"raw_dy,omitempty"`
	// WarpPinned says the place is the lock's last warp, MyGo not having
	// moved since; WarpSeq is that warp's number.
	WarpPinned bool   `json:"warp_pinned,omitempty"`
	WarpSeq    uint32 `json:"warp_seq"`
	Mode       string `json:"mode"`
	SentKind   string `json:"sent_kind"`
	SentX      uint16 `json:"sent_x,omitempty"`
	SentY      uint16 `json:"sent_y,omitempty"`
	SentDX     int32  `json:"sent_dx,omitempty"`
	SentDY     int32  `json:"sent_dy,omitempty"`
	SentCount  int    `json:"sent_count,omitempty"`
	Button     string `json:"button,omitempty"`
	Pressed    *bool  `json:"pressed,omitempty"`
	Suppress   string `json:"suppress_reason,omitempty"`
}

// mode is how the lock holds the pointer, free without one.
func (f *inputForwarder) mode() pointerMode {
	if f.lock == nil {
		return pointerFree
	}
	return f.lock.held()
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

// droppedEdge is why buttonsFrame sent nothing of e.
func droppedEdge(e buttonEdge, active, held, sideIn bool) string {
	switch {
	case e.cancel:
		return "cancel"
	case !e.down:
		return "not_held"
	case !active:
		return "inactive"
	case held:
		return "held"
	case !e.in:
		return "off_picture"
	case e.side && !sideIn:
		return "side_off_picture"
	}
	return "unknown"
}

func (f *inputForwarder) traceDroppedButton(e buttonEdge, why string) {
	if !itrace.On() {
		return
	}
	down := e.down
	r := clientInputRec{Event: "button", Mode: f.mode().String(), SentKind: "suppressed", Button: buttonName(e.b), Pressed: &down, Suppress: why}
	if e.cancel {
		r.Button, r.Pressed = "", nil
	}
	inputtrace.Emit(itrace, "client_input", r)
}

// frameKey is what traceFrame compares to write a frame's pointer only
// when it changed.
type frameKey struct {
	rawX, rawY     float32
	rawOver        bool
	pinned         bool
	warpSeq        uint32
	mode           string
	kind, suppress string
	sentX, sentY   uint16
	// written tells the first frame from the zero key.
	written bool
}

// traceFrame writes where MyGo had the pointer this frame and what went
// to the host for it, when it sent a move or any of it changed.
func (f *inputForwarder) traceFrame(rawX, rawY float32, rawOver, relative, hasView bool, at framePointer, wants, sent bool) {
	r := clientInputRec{Event: "pointer", RawX: &rawX, RawY: &rawY, RawOn: &rawOver, WarpPinned: f.warp.on, WarpSeq: f.warp.n,
		Mode: f.mode().String(), SentKind: "suppressed"}
	switch {
	case relative:
		r.Suppress = "relative"
	case !hasView:
		r.Suppress = "no_view"
	case !at.ok:
		r.Suppress = "not_over"
	case !wants:
		r.Suppress = "off_picture"
	case !sent:
		r.Suppress = "unchanged"
	default:
		r.SentKind = "abs"
	}
	if at.ok {
		r.SentX, r.SentY = at.x, at.y
	}
	k := frameKey{rawX, rawY, rawOver, r.WarpPinned, r.WarpSeq, r.Mode, r.SentKind, r.Suppress, r.SentX, r.SentY, true}
	if r.SentKind == "abs" || r.Suppress == "unchanged" {
		k.kind, k.suppress = "", ""
	}
	if !sent && k == f.lastFrame {
		return
	}
	f.lastFrame = k
	inputtrace.Emit(itrace, "client_input", r)
}

// traceMeta writes session_meta as tracing turns on, a stream starts, or
// the window, the view or the picture changes.
func (f *inputForwarder) traceMeta(view traceArea, fw, fh, dw, dh int, active, relative bool) {
	pic := pictureRect(view.W, view.H, fw, fh, dw, dh)
	pic.X, pic.Y = pic.X+view.X, pic.Y+view.Y
	m := sessionMeta{VideoW: fw, VideoH: fh, DrawW: dw, DrawH: dh, View: view, Picture: areaOf(pic), Focused: active, Relative: relative}
	if f.meta != nil {
		m.windowMeta = f.meta()
	} else {
		m.Platform = platformName()
	}
	if e := itrace.Epoch(); e == f.metaEpoch && m == f.lastMeta {
		return
	}
	f.metaEpoch, f.lastMeta = itrace.Epoch(), m
	inputtrace.Emit(itrace, "session_meta", m)
}
