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
	"github.com/planckdotgg/planck/pkg/gamepad"
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
	// Display is how this client's screen shows the stream.
	Display         DisplayPhase
	BitrateMbps     float64
	FECPct, LossPct float64
	PathKind        discovery.PathKind
	Codec           encode.Codec
	Chroma          encode.Chroma
	Width, Height   int
	AudioBufMs      float64
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
	// Display is how this client's screen shows the stream.
	Display DisplayPhase
}

// DisplayPhase is how the client's screen shows the stream: scanned out
// directly, or composited first, which costs a refresh; and how the
// pictures line up with its refreshes (pkg/phaselock).
type DisplayPhase struct {
	// ScanOut is "direct" or "composited", empty while unknown.
	ScanOut string
	// RefreshHz is the screen's measured refresh, 0 while unknown.
	RefreshHz float64
	// Locked: the pictures come within half a millisecond of the target
	// before a refresh. ErrorMs is how early they come past it, below 0
	// late. Reported says the host takes the measurements; it steers by
	// them only where its source can move its frames.
	Locked   bool
	ErrorMs  float64
	Reported bool
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

// GamepadSettings are how this device processes its controllers and,
// while it hosts, what clients' controllers become, kept in gamepad.json.
type GamepadSettings struct {
	// LeftDeadzone and RightDeadzone are radial stick deadzones, and
	// TriggerThreshold the trigger travel ignored, in percent: 0 to 50.
	LeftDeadzone     int
	RightDeadzone    int
	TriggerThreshold int
	InvertLeftY      bool
	InvertRightY     bool
	// RumbleEnabled plays a host's rumble here at RumbleStrength percent.
	RumbleEnabled  bool
	RumbleStrength int
	// VirtualType is what clients' controllers appear as to games while
	// this machine hosts: one of the VirtualPad constants.
	VirtualType        string
	AllowGuestGamepads bool
}

// Virtual controller types, as GamepadSettings.VirtualType holds them.
const (
	VirtualPadXbox360   = config.VirtualPadXbox360
	VirtualPadDS4       = config.VirtualPadDS4
	VirtualPadDualSense = config.VirtualPadDualSense
)

// GamepadState is a controller's input after mapping and calibration: what
// a stream sends.
type GamepadState = gamepad.State

// GamepadInfo is one controller attached to this device.
type GamepadInfo struct {
	// ID names it to the mapping methods while it stays attached.
	ID   string
	Name string
	GUID string
	// Transport is "USB", "Bluetooth", "Wireless", or "" when unknown.
	Transport string
	// BatteryPct is -1 when unknown or wired.
	BatteryPct int
	// Slot is the pad index a stream sends it as, from 0.
	Slot  int
	State GamepadState
	// Buttons, Axes (-1 to 1) and Hats are its raw inputs, as an SDL
	// mapping indexes them.
	Buttons []bool
	Axes    []float64
	Hats    []uint8
	// Mapping is the SDL gamecontrollerdb line in use; Custom says it is
	// one the user saved rather than the controller's default.
	Mapping string
	Custom  bool
	// Learning is the input Map waits on, by SDL name, "" when none.
	Learning string
}

// VirtualPadStatus is whether this machine can plug in virtual
// controllers while it hosts.
type VirtualPadStatus = gamepad.PadDriverStatus

// HostGamepadSlot is one virtual controller slot of this machine while it
// hosts: Pad is the client controller in it, by its pad index, -1 when
// free.
type HostGamepadSlot struct {
	Slot int
	Pad  int
}

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

// ErrOneDisplay is SwitchDisplay's answer from a host with one display.
var ErrOneDisplay = errors.New("this host has one display")

// ErrNoDisplaySwitch is the answer of SwitchDisplay and
// SelectHostDisplay from a host whose Planck cannot move a stream to
// another display.
var ErrNoDisplaySwitch = errors.New("this host cannot switch displays; update Planck on it")

// DisplayInfo is a display of this machine, or of a host.
type DisplayInfo struct {
	ID string
	// Name is what the display's OS calls it, "" without one.
	Name          string
	Width, Height int
	RefreshHz     int
	Primary       bool
	// HDR: the desktop on the display is in HDR.
	HDR bool
}

// MinStreamWindows and MaxStreamWindows bound Settings.MaxStreamWindows.
const (
	MinStreamWindows = config.MinStreamWindows
	MaxStreamWindows = config.MaxStreamWindows
)

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
	// OpenEveryHostDisplay opens a window for each of a host's displays,
	// up to MaxStreamWindows, where the host streams them at once.
	OpenEveryHostDisplay bool
	MaxStreamWindows     int
	// SwapCmdCtrl trades Cmd and Ctrl in what goes to a host whose OS is
	// macOS when this one's is not, or the other way.
	SwapCmdCtrl bool
	// OutputDevice and MicDevice are what a stream plays on and records
	// from, by AudioDevice.ID; empty, or a device unplugged, is the
	// system's default. A change applies from the next stream.
	OutputDevice string
	MicDevice    string
	// UsageTelemetry sends anonymous usage counts (internal/telemetry);
	// a change applies at once.
	UsageTelemetry bool
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
	// SwitchDisplay moves the stream to the host's next display, in the
	// order HostDisplays lists them; ErrOneDisplay from a host with one,
	// ErrNoDisplaySwitch from one that cannot.
	SwitchDisplay() error
	// HostDisplays lists the displays of the host hostID (a HostInfo.ID),
	// as it advertised them while streaming this run; nil before.
	HostDisplays(hostID string) []DisplayInfo
	// SelectHostDisplay moves the stream to the streaming host's display
	// id and keeps it as the display that host's streams start on.
	SelectHostDisplay(id string) error

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

	// GamepadSettings are gamepad.json's; SetGamepadSettings keeps them and
	// applies them to the stream and to hosting at once.
	GamepadSettings() GamepadSettings
	SetGamepadSettings(s GamepadSettings) error
	// Gamepads lists the controllers attached now, read in the background
	// for a second after each call.
	Gamepads() []GamepadInfo
	// MapGamepadButton waits up to ten seconds for a control of controller
	// id to move and binds it to target, an SDL input name such as "a" or
	// "lefttrigger"; GamepadInfo.Learning shows the wait. UnmapGamepadButton
	// clears target, ResetGamepadMapping forgets the saved mapping, and
	// SetGamepadSDLMapping saves a gamecontrollerdb line for the controller.
	MapGamepadButton(id, target string) error
	UnmapGamepadButton(id, target string) error
	ResetGamepadMapping(id string) error
	SetGamepadSDLMapping(id, sdl string) error
	// TestGamepadRumble shakes controller id briefly at the rumble strength.
	TestGamepadRumble(id string) error
	// VirtualPadStatus is the virtual controller driver of this machine.
	VirtualPadStatus() VirtualPadStatus
	// HostGamepadSlots are the four virtual controller slots while hosting,
	// nil when not. ResetGamepadSlots unplugs them all; ReorderGamepadSlot
	// swaps the controllers in two slots, so a game's player order changes.
	HostGamepadSlots() []HostGamepadSlot
	ResetGamepadSlots() error
	ReorderGamepadSlot(from, to int) error

	// StartCalibration measures the route to hostID, a paired HostInfo.ID,
	// and picks the stream settings for it, in the background for about 40
	// seconds; Calibration reports it as it goes, and onChange hears each
	// step. Its display phase shows the stream in the stream window, which
	// it opens. It refuses with ErrCalibrating while a calibration runs and
	// ErrStreaming while a stream does; Connect refuses while it runs.
	StartCalibration(hostID string) error
	// CancelCalibration ends the running calibration at once; its state
	// then says Cancelled.
	CancelCalibration()
	// Calibration is the calibration running now, or the last one.
	Calibration() CalibrationState
	// ApplyCalibration keeps the last finished calibration of hostID for
	// the route it ran on, so streams there take its settings wherever the
	// overrides are auto; ErrNoCalibration without one.
	ApplyCalibration(hostID string) error
	// ResetCalibration forgets what was kept for hostID on routes, or on
	// every route with none, so its streams take the defaults again.
	ResetCalibration(hostID string, routes ...discovery.PathKind) error
	// CalibrationStatus lists what is kept for hostID, one per route, LAN
	// first; nil when nothing is.
	CalibrationStatus(hostID string) []CalibrationSummary
}
