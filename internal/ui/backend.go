package ui

import (
	"context"
	"errors"

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
}

// Settings are the client's stream preferences. SaveSettings applies them
// to a live stream where the host allows.
type Settings struct {
	Codec  encode.Codec
	Chroma encode.Chroma
	// TargetFPS 0 matches the client's display.
	TargetFPS      int
	MaxBitrateMbps int
	// Slices is how many slices each frame is encoded in; 1 is whole
	// frames. SubFrame sends each slice as soon as it is encoded.
	Slices            int
	SubFrame          bool
	AudioEnabled      bool
	SystemKeysCapture bool
	RelativeMouse     bool
	PreferLAN         bool
	// Encoder is the GPU this machine encodes on while hosting, by
	// EncoderInfo.GPU; empty lets the host choose.
	Encoder string
	// HostName is what this machine is called while hosting; empty is
	// its own name.
	HostName string
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

	Hosting() HostingState
	SetHosting(ctx context.Context, on bool) error

	Tailnet() TailnetStatus
	Encoders() []EncoderInfo
	Settings() Settings
	SaveSettings(s Settings) error

	Pairing() PairState
	SubmitPIN(ctx context.Context, pin string) error
	TrustTailscalePeer(ctx context.Context) error
	CancelPairing()
}
