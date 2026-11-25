package ui

import (
	"context"
	"errors"
	"time"

	"github.com/planckdotgg/planck/pkg/audio"
	"github.com/planckdotgg/planck/pkg/config"
	"github.com/planckdotgg/planck/pkg/defaults"
	"github.com/planckdotgg/planck/pkg/discovery"
	"github.com/planckdotgg/planck/pkg/encode"
)

// ErrPairingRequired is what Backend.Connect returns for a host this
// client has not paired with. The backend sets Pairing to the request
// before returning it, and the app opens the Pairing screen.
var ErrPairingRequired = errors.New("pairing required")

// Quick Connect errors: Connect returns them for text that names no host
// it can reach.
var (
	// ErrUnknownHost is a name or peer fingerprint no host found or
	// remembered has.
	ErrUnknownHost = errors.New("no host by that name or fingerprint")
	// ErrShareLink is a planck.gg/j/ share link, which needs Planck Cloud:
	// this build cannot open one.
	ErrShareLink = errors.New("share links need Planck Cloud, which this build cannot reach")
)

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
	// Fingerprint is the host certificate's SHA-256, hex encoded, as it
	// was found or pinned; Quick Connect takes it as a peer ID.
	Fingerprint string
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
	Profile   *defaults.ResolvedProfile
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

// StageStat is one pipeline stage's latency in milliseconds; 0 is not
// measured.
type StageStat struct {
	P50, P99 float64
}

// SessionStats are the statistics of a stream, sampled four times a
// second: the Statistics page's live view, and with LastSession the final
// sample of the last stream.
type SessionStats struct {
	// Live is false without a stream; LastSession's is false too.
	Live     bool
	HostName string
	HostID   string
	// Started is when the stream started and Ended when it ended, zero
	// while it runs.
	Started, Ended time.Time

	// The stages, in pipeline order. Capture is the host's, zero from a
	// host that does not report it, whose Encode then includes it; a host
	// that splits them reports p50s only. Total is glass to glass and may
	// exceed the sum of the stages.
	Capture, Encode, Network, Decode, Present, Total StageStat
	// FrameTimesMs are the gaps between the last frames decoded, oldest
	// first, at most session.FrameTimeRing of them.
	FrameTimesMs []float64
	FPS          float64
	TargetFPS    int
	// FramesReceived counts the frames that arrived whole, FramesDropped
	// those this client dropped, and FramesLost those lost on the route
	// that parity could not rebuild.
	FramesReceived, FramesDropped, FramesLost, Keyframes uint64

	BitrateMbps float64
	// CeilingMbps is the most the host accepted on the route; 0 is
	// unknown.
	CeilingMbps float64
	// LossPct is the share of frames lost; FECParityPct the parity sent
	// over the data, and FECRepaired the slices parity rebuilt.
	LossPct, FECParityPct float64
	FECRepaired           uint64

	AudioBufMs float64
	// MicMuted, MicDevice and MicHint are as in Telemetry.
	MicMuted  bool
	MicDevice string
	MicHint   string

	Codec         encode.Codec
	Chroma        encode.Chroma
	BitDepth      int
	Width, Height int
	// EncoderGPU and EncoderAPI are the host's, from the profile; empty
	// from a host that does not resolve streams.
	EncoderGPU, EncoderAPI string
	PathKind               discovery.PathKind
	RTTMs                  float64
	// DatagramBytes is the largest media datagram on the route.
	DatagramBytes int
	// Profile is what the host resolved the stream to, nil from a host
	// that does not resolve streams.
	Profile *defaults.ResolvedProfile
	// HostPolicy is what the host's owner forces, which beats this
	// client's overrides: a knob not auto here shows as Set by host.
	HostPolicy defaults.UserOverrides
}

// ClipboardDirection is what a host shares of the clipboard.
type ClipboardDirection string

const (
	ClipboardOff          ClipboardDirection = "off"
	ClipboardBoth         ClipboardDirection = "both"
	ClipboardHostToClient ClipboardDirection = "host-to-client"
	ClipboardClientToHost ClipboardDirection = "client-to-host"
)

// HostPorts are a host's UDP ports. Control and Media 0 pick a free one;
// Discovery 0 turns LAN discovery off.
type HostPorts struct {
	Control, Media, Discovery int
}

// PortStatus is TestPorts' answer for each port: "" when this machine
// can host on it, else why not.
type PortStatus struct {
	Control, Media, Discovery string
}

// HostSettings are what this machine does when other devices stream from
// it, kept in host.json.
type HostSettings struct {
	// Name is what clients see; empty is the machine's name.
	Name string
	// Encoder is the GPU it encodes on, by EncoderInfo.GPU; empty lets the
	// host choose.
	Encoder string
	// Display is the display it captures, by DisplayInfo.ID; empty is the
	// primary one.
	Display string
	// Policy is what clients cannot go above: a knob not auto here beats
	// every client's override.
	Policy defaults.UserOverrides
	// VDDPrimary makes a session's virtual display the primary one.
	VDDPrimary bool
	// FixedPIN is a pairing PIN that never changes; empty rotates a new
	// one after each pairing.
	FixedPIN string
	// RequirePINForLAN pairs an unpaired device on the LAN only by the
	// PIN; AllowTailscaleSameUser pairs a device of this machine's own
	// tailnet user, on the tailnet, with none.
	RequirePINForLAN       bool
	AllowTailscaleSameUser bool
	// AllowInput lets clients' keyboard, mouse and gamepad drive this
	// machine.
	AllowInput bool
	Clipboard  ClipboardDirection
	Ports      HostPorts

	// AudioDevice is the output whose sound streams, by AudioDevice.ID
	// (Windows); empty is everything this machine plays but Planck.
	AudioDevice string
	// ExcludeApps are apps left out of the streamed sound, by name, such
	// as "Discord" (macOS hosts; RunningApps lists candidates). It is an
	// AppList so that HostSettings stays comparable.
	ExcludeApps AppList
	// VirtualMic is where a client's microphone plays: empty finds a
	// virtual mic, "off" refuses it, anything else is an output's ID or
	// part of its name.
	VirtualMic string
	// The session settings apply to the stream running at once, but for
	// PrivacyMode, which applies from the next stream.
	StayAwake        bool
	LockOnDisconnect bool
	MuteSpeakers     bool
	// PrivacyMode turns this machine's displays off while a client streams
	// from a virtual display; PrivacyModeProblem says why it cannot.
	PrivacyMode bool
	// IdleTimeoutMinutes disconnects a client idle that long; 0 is never.
	IdleTimeoutMinutes int
}

// VirtualMicOff is HostSettings.VirtualMic for no microphone.
const VirtualMicOff = config.VirtualMicOff

// AppList holds app names; NewAppList makes one and Names reads it.
type AppList = config.AppList

// NewAppList keeps the names that are not blank, once each, trimmed.
func NewAppList(names ...string) AppList { return config.NewAppList(names...) }

// AudioDevice is a sound device of this machine.
type AudioDevice struct {
	// ID is stable across runs.
	ID        string
	Name      string
	IsDefault bool
}

// AudioDevices are this machine's outputs and inputs.
type AudioDevices struct {
	Outputs, Inputs []AudioDevice
}

// VirtualAudioStatus is Planck Virtual Audio's driver and device on this
// machine.
type VirtualAudioStatus = audio.VirtualAudioStatus

// HotkeyAction names what a hotkey does.
type HotkeyAction = config.HotkeyAction

// The hotkeys, in the order Settings lists them: the stream's, then the
// host's, which are global.
const (
	HotkeyStreamMenu    = config.HotkeyStreamMenu
	HotkeyHideButton    = config.HotkeyHideButton
	HotkeyMetrics       = config.HotkeyMetrics
	HotkeyCycleHUD      = config.HotkeyCycleHUD
	HotkeyMicrophone    = config.HotkeyMicrophone
	HotkeyFullscreen    = config.HotkeyFullscreen
	HotkeyImmersive     = config.HotkeyImmersive
	HotkeyCursorLock    = config.HotkeyCursorLock
	HotkeySwitchDisplay = config.HotkeySwitchDisplay
	HotkeyCtrlAltDel    = config.HotkeyCtrlAltDel
	HotkeyKeyframe      = config.HotkeyKeyframe
	HotkeyDisconnect    = config.HotkeyDisconnect
	HotkeyStopHosting   = config.HotkeyStopHosting
	HotkeyKickClient    = config.HotkeyKickClient
)

// Hotkey is one hotkey. Chords are written as "Ctrl+Alt+Shift+Super+K"
// (Super is Cmd or the Windows key); ChordLabel shows one.
type Hotkey struct {
	Action HotkeyAction
	// Chord is "" while unbound; Default is what ResetHotkey gives back.
	Chord, Default string
	// Global is a host action, which fires while Planck is not in front.
	Global bool
}

// HotkeyError is a chord SetHotkey or AddNeverSend refused: errors.Is
// tells ErrNoModifier, ErrReservedChord (Reason says what the system does
// with it) and ErrChordInUse (Owner is the hotkey that has it, empty when
// the never-send list does).
type HotkeyError = config.HotkeyError

var (
	ErrBadChord      = config.ErrBadChord
	ErrNoModifier    = config.ErrNoModifier
	ErrReservedChord = config.ErrReservedChord
	ErrChordInUse    = config.ErrChordInUse
	ErrNotGlobal     = config.ErrNotGlobal
	// ErrDesktopRefused is a host hotkey's chord the desktop would not
	// register while hosting; Reason is the desktop's answer.
	ErrDesktopRefused = config.ErrDesktopRefused
)

// ErrOneDisplay is SwitchDisplay's answer while a host streams one
// display, as every host does until multi-display streams.
var ErrOneDisplay = errors.New("this host streams one display")

// DisplayInfo is a display of this machine.
type DisplayInfo struct {
	ID            string
	Width, Height int
	RefreshHz     int
	Primary       bool
}

// PairedClient is a device this machine trusts to stream from it.
type PairedClient struct {
	Name string
	// Fingerprint is the client certificate's SHA-256, hex encoded.
	Fingerprint string
	PairedAt    time.Time
	// LastSeen is when it last connected; zero is never since pairing was
	// recorded.
	LastSeen time.Time
	// TrustedViaTailnet is set for a device paired by its Tailscale
	// identity, without the PIN.
	TrustedViaTailnet bool
	// Connected is set while it streams from this machine.
	Connected bool
}

// Settings are the client's preferences other than the stream's format,
// which the overrides hold. SaveSettings applies them to a live stream.
type Settings struct {
	AudioEnabled bool
	// ClipboardSend shares this device's clipboard with the host it
	// streams from; ClipboardReceive applies the host's here. What this
	// machine shares while hosting is HostSettings.Clipboard.
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
	// OpenFullscreen opens each stream's window in full screen.
	OpenFullscreen bool
	// SwapCmdCtrl trades Cmd and Ctrl in what goes to a host whose OS is
	// macOS when this one's is not, or the other way.
	SwapCmdCtrl bool
	// OutputDevice and MicDevice are what a stream plays on and records
	// from, by AudioDevice.ID; empty, or a device unplugged, is the
	// system's default. A change applies from the next stream.
	OutputDevice string
	MicDevice    string
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

// OverlayLayout is where the stream overlay was left, anchored to the
// stream view's edges or centre, so that another window size keeps it in
// place.
type OverlayLayout struct {
	// Placed is false before the first stream.
	Placed bool
	Dock   DockEdge
	// DockFrac is the docked tab's centre along its edge, a fraction of
	// it; ButtonPlace is where the button floats when not docked.
	DockFrac    float32
	ButtonPlace Placement
	Metrics     bool
	// MetricsPlace is unset until the metrics window was first shown.
	MetricsPlace   Placement
	MetricsCompact bool
	// ButtonX and ButtonY (the button's centre) and MetricsX and MetricsY
	// (the window's top-left corner) are fractions of the view from layouts
	// stored before placements; a stream starts from them only when the
	// placement is unset.
	ButtonX, ButtonY   float32
	MetricsX, MetricsY float32
}

// WindowHook is the window the client streams in, which the app that
// made it supplies (SetWindow).
type WindowHook interface {
	Fullscreen() bool
	SetFullscreen(on bool)
}

// StreamWindowHook opens and closes the window a stream shows in, apart
// from the launcher's, which the app that made the launcher supplies
// (SetStreamWindow). The UI calls it on the main thread; the window shows
// App.StreamContent.
type StreamWindowHook interface {
	// OpenStreamWindow opens the window for a stream from host, in full
	// screen if Settings.OpenFullscreen, or names the open one after host
	// and brings it to the front.
	OpenStreamWindow(host string)
	// CloseStreamWindow leaves full screen, waits for the stream's video
	// to let the window go, and closes it.
	CloseStreamWindow()
	// StreamWindowOpen reports whether the window is open and not closing.
	StreamWindowOpen() bool
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

// SystemKeysHook is immersive mode's capture of the system's own keys,
// which the app that streams supplies (SetSystemKeys). The UI calls it on
// the main thread.
type SystemKeysHook interface {
	// SystemKeysProblem is why the system's keys cannot be captured, such
	// as a permission the OS wants, "" while they can.
	SystemKeysProblem() string
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
	// PIN is the pairing PIN to show while On, and Fingerprint the host
	// certificate's SHA-256, hex encoded, which clients check.
	PIN         string
	Fingerprint string
	// Using are the ports bound while On: Media 0 while each session
	// picks one, Discovery 0 with discovery off.
	Using HostPorts
	// Client names the device streaming from this machine, if any.
	Client string
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
	// Address is the host's control address, and PathKind and RTTMs the
	// route to it as discovery found it; RTTMs 0 is not measured.
	Address  string
	PathKind discovery.PathKind
	RTTMs    float64
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
	// RemoveHost forgets a host on this client: its pairing, its local
	// name and its stream overrides, and its card at once. A host still
	// announcing itself comes back, unpaired.
	RemoveHost(id string) error
	// Connect streams from target, a HostInfo.ID or what was typed in
	// Quick Connect, returning once the stream runs or fails. Quick
	// Connect takes an IP or host:port, a host's name or alias, a
	// Tailscale MagicDNS name, a host certificate fingerprint, or a
	// planck.gg/j/ share link. A host to pair with returns
	// ErrPairingRequired; text that names no host returns ErrUnknownHost
	// or ErrShareLink.
	Connect(ctx context.Context, target string) error
	Disconnect()
	Telemetry() Telemetry
	// SessionStats are the stream's statistics now; Live is false without
	// one.
	SessionStats() SessionStats
	// LastSession is the final sample of the last stream that ended,
	// false before one has.
	LastSession() (SessionStats, bool)
	RequestKeyframe()
	// SetMicMuted mutes or unmutes this client's microphone on the host.
	SetMicMuted(muted bool) error

	Hosting() HostingState
	SetHosting(ctx context.Context, on bool) error
	// HostSettings are this machine's hosting settings; SetHostSettings
	// keeps them, and restarts hosting, ending its stream, when a change
	// needs it (anything but the name).
	HostSettings() HostSettings
	SetHostSettings(s HostSettings) error
	// Displays lists this machine's displays, to capture while hosting.
	Displays() []DisplayInfo
	// MeasureEncoder encodes a short synthetic stream on gpu, an
	// EncoderInfo.GPU, and returns its p50 encode time in milliseconds,
	// which Encoders then reports. It takes about a second.
	MeasureEncoder(ctx context.Context, gpu string) (float64, error)
	// TestPorts checks whether this machine can host on p; the ports
	// hosting holds now pass.
	TestPorts(p HostPorts) PortStatus
	// PairedClients lists the devices this machine trusts, newest first.
	// RevokeClient forgets one by fingerprint and RevokeAllClients every
	// one, ending a stream to it; they pair again with the PIN.
	PairedClients() []PairedClient
	RevokeClient(fingerprint string) error
	RevokeAllClients() error
	// ClientName is the name hosts pair this device under, and
	// SetClientName keeps another; empty goes back to the machine's name.
	ClientName() string
	SetClientName(name string) error

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

	// Hotkeys lists every hotkey in order. SetHotkey binds one, or with
	// "" unbinds it, returning a *HotkeyError for a chord it refuses;
	// ResetHotkey gives one its default back and ResetAllHotkeys every
	// one.
	Hotkeys() []Hotkey
	SetHotkey(action HotkeyAction, chord string) error
	ResetHotkey(action HotkeyAction) error
	ResetAllHotkeys() error
	// NeverSend are chords never sent to the host, even in immersive
	// mode, such as a voice chat's push-to-talk key; a bare key counts.
	// AddNeverSend refuses a hotkey's chord with a *HotkeyError.
	NeverSend() []string
	AddNeverSend(chord string) error
	RemoveNeverSend(chord string) error
	// SendCtrlAltDel sends Ctrl+Alt+Del to the host, which a Windows host
	// answers with its security screen where Windows lets it.
	SendCtrlAltDel()
	// SwitchDisplay moves the stream to the host's next display;
	// ErrOneDisplay while hosts stream one.
	SwitchDisplay() error

	// KickClient disconnects the client streaming from this machine,
	// which may connect again.
	KickClient() error
	// AudioDevices lists this machine's sound devices, for Settings'
	// output, microphone and capture pickers.
	AudioDevices() AudioDevices
	// VirtualAudio is whether Planck Virtual Audio is installed and
	// running, and the virtual mic Auto picks.
	VirtualAudio() VirtualAudioStatus
	// RunningApps names the apps running now, for the apps hosting leaves
	// out of the sound.
	RunningApps() []string
	// PrivacyModeProblem is why privacy mode cannot work on this machine,
	// "" when it can.
	PrivacyModeProblem() string
}
