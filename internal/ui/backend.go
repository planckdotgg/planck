package ui

import (
	"context"
	"errors"

	"github.com/planckdotgg/planck/pkg/defaults"
	"github.com/planckdotgg/planck/pkg/discovery"
	"github.com/planckdotgg/planck/pkg/encode"
)

// ErrPairingRequired is what Backend.Connect returns for a host this
// client has not paired with. The backend sets Pairing to the request
// before returning it, and the app opens the Pairing screen.
var ErrPairingRequired = errors.New("pairing required")

// HostInfo is one host found on the LAN or the tailnet, or remembered:
// one per machine, with every route it was found on.
type HostInfo struct {
	ID string
	// Name is Alias when set, else AdvertisedName.
	Name           string
	AdvertisedName string
	// Alias is the name this client gave the host with RenameHost.
	Alias    string
	OS       string
	GPU      string
	PathKind discovery.PathKind
	// RTTMs is the measured round trip; 0 means not measured yet.
	RTTMs             float64
	Online            bool
	Paired            bool
	Connected         bool
	TailscaleSameUser bool
	// Address is the control address of the best route.
	Address string
	// LANAddress and TailscaleAddress are the host's control addresses on
	// each kind of route, found or remembered; Reachable says the host
	// answered on it lately.
	LANAddress         string
	TailscaleAddress   string
	LANReachable       bool
	TailscaleReachable bool
	// Width, Height and RefreshHz describe the host's display; 0 means
	// the host did not report it.
	Width, Height int
	RefreshHz     int
}

// Telemetry is the live state of the stream, sampled once a frame.
type Telemetry struct {
	Connected bool
	HostName  string
	// HostID keys the host's own overrides; empty for a host reached by
	// address that sent none.
	HostID string
	// Profile is what the host resolved the stream to; nil from a host
	// that does not resolve streams.
	Profile *defaults.ResolvedProfile
	// Display is the client display the stream was resolved for, whose
	// aspect the resolution presets take.
	Display   defaults.ClientDisplay
	FPS       float64
	TargetFPS int
	// Stage times are p50 over the last second, in milliseconds. TotalMs
	// is glass to glass and may exceed the sum of the stages.
	EncodeMs, NetMs, DecodeMs, PresentMs, TotalMs float64
	BitrateMbps                                   float64
	FECPct, LossPct                               float64
	PathKind                                      discovery.PathKind
	Codec                                         encode.Codec
	Chroma                                        encode.Chroma
	Width, Height                                 int
	AudioBufMs                                    float64
	// DatagramBytes is the largest media datagram on the route.
	DatagramBytes int
	// MicMuted is whether this client's microphone is muted on the host,
	// as every stream starts. MicDevice is the host's virtual mic it plays
	// into; MicHint says why there is none.
	MicMuted  bool
	MicDevice string
	MicHint   string

	// CaptureMs is the host's capture stage, 0 from a host that does not
	// report it, whose EncodeMs then holds it.
	CaptureMs float64
	// RTTMs is the transport's round trip, where NetMs is one way.
	RTTMs float64
	// CeilingMbps is the most the host accepted on the route; 0 is
	// unknown.
	CeilingMbps float64
	// FramesDropped counts the frames this client dropped since the
	// stream started.
	FramesDropped uint64
}

// Settings are the client's preferences other than the stream's format,
// which the overrides hold. SaveSettings applies them to a live stream.
type Settings struct {
	AudioEnabled bool
	// ClipboardSend shares this device's clipboard with the other end of
	// a stream; ClipboardReceive applies the other end's here.
	ClipboardSend     bool
	ClipboardReceive  bool
	SystemKeysCapture bool
	RelativeMouse     bool
	// Encoder is the GPU this machine encodes on while hosting, by
	// EncoderInfo.GPU; empty lets the host choose.
	Encoder string
	// HostName is what this machine is called while hosting; empty is
	// its own name.
	HostName string
	// AudioVolume is the stream's playback level from 0 to 1; AudioMuted
	// silences it and keeps the audio stream running.
	AudioVolume float64
	AudioMuted  bool
}

// DockEdge is the stream view's edge the overlay button is docked to.
type DockEdge uint8

const (
	DockNone DockEdge = iota
	DockLeft
	DockRight
	DockTop
	DockBottom
)

// OverlayLayout is where the stream overlay was left, in fractions of the
// stream view, so that another window size keeps it in place.
type OverlayLayout struct {
	// Placed is false before the first stream.
	Placed bool
	Dock   DockEdge
	// DockFrac is the docked tab's centre along its edge; ButtonX and
	// ButtonY are the floating button's centre.
	DockFrac         float32
	ButtonX, ButtonY float32
	Metrics          bool
	// MetricsX and MetricsY are the metrics window's top-left corner, both
	// 0 until it was first shown.
	MetricsX, MetricsY float32
	MetricsCompact     bool
}

// WindowHook is the window the client streams in, which the app that
// made it supplies (SetWindow).
type WindowHook interface {
	Fullscreen() bool
	SetFullscreen(on bool)
}

// CursorHook is the stream's mouse lock, which the app that streams
// supplies (SetCursor). The UI calls it on the main thread.
type CursorHook interface {
	// CursorLocked reports whether the mouse is to be locked to the
	// stream, by a game that captured it or by the user; it engages while
	// the window has the focus and the stream's menu is closed.
	CursorLocked() bool
	// SetCursorLocked locks the mouse, or releases it until a click on the
	// picture or SetCursorLocked(true).
	SetCursorLocked(on bool)
	// SetMenuOpen frees the mouse while the stream's menu is open.
	SetMenuOpen(open bool)
	// StreamEnded frees the mouse and forgets the stream's lock.
	StreamEnded()
}

// EncoderInfo is a hardware encoder on this machine.
type EncoderInfo struct {
	GPU string
	API string
	// EncodeMs is the measured p50 encode time; 0 means not measured.
	EncodeMs float64
}

// HostingState is whether this machine is streaming its own screen.
type HostingState struct {
	On bool
	// Encoder names the GPU and API in use or that would be, such as
	// "M5 Pro VideoToolbox".
	Encoder string
	Err     string
}

// TailnetStatus is the local Tailscale client as LocalAPI reports it.
type TailnetStatus struct {
	Running bool
	User    string
}

// PairStatus is where a pairing request stands.
type PairStatus int

const (
	PairNone PairStatus = iota
	PairPending
	PairVerifying
	PairPaired
	PairFailed
)

// PairState is the pairing request in progress, if any. A same-user
// tailnet peer pairs without a PIN; anyone else needs the six-digit PIN
// the host shows and a check of the host's certificate fingerprint.
type PairState struct {
	Status            PairStatus
	HostName          string
	TailscaleSameUser bool
	// Fingerprint is the host certificate's SHA-256, hex encoded.
	Fingerprint string
	Error       string
}

// Backend is what the client UI drives. The UI reads its state on the
// main thread once a frame, so every method must be safe from any
// goroutine. Methods that take a context may block; the UI calls them off
// the main thread.
type Backend interface {
	// Subscribe registers the UI's redraw request. Call onChange, from any
	// goroutine, whenever a value a getter returns changes.
	Subscribe(onChange func())

	// Hosts lists the hosts: connected first, then online, then by Name.
	Hosts() []HostInfo
	RefreshHosts()
	// RenameHost names a host on this client only; an empty alias goes
	// back to the name the host advertises.
	RenameHost(id, alias string) error
	// Connect streams from target, a HostInfo.ID or an address typed in
	// Quick Connect, returning once the stream runs or fails.
	Connect(ctx context.Context, target string) error
	Disconnect()
	Telemetry() Telemetry
	RequestKeyframe()
	// SetMicMuted mutes or unmutes this client's microphone on the host.
	SetMicMuted(muted bool) error

	Hosting() HostingState
	SetHosting(ctx context.Context, on bool) error

	Tailnet() TailnetStatus
	Encoders() []EncoderInfo
	Settings() Settings
	SaveSettings(s Settings) error
	// Overrides returns the stream overrides kept for hostID, or the ones
	// for every host when hostID is "". A host's auto knobs follow the
	// ones for every host.
	Overrides(hostID string) defaults.UserOverrides
	// SaveOverrides keeps o for hostID, or for every host when hostID is
	// "", and restarts a live stream they change.
	SaveOverrides(hostID string, o defaults.UserOverrides) error
	// OverlayLayout is where the stream overlay was left last, and
	// SaveOverlayLayout keeps l for the next stream.
	OverlayLayout() OverlayLayout
	SaveOverlayLayout(l OverlayLayout) error

	Pairing() PairState
	SubmitPIN(ctx context.Context, pin string) error
	TrustTailscalePeer(ctx context.Context) error
	CancelPairing()
}
