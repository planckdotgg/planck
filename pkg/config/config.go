// Package config persists Planck host and client settings as JSON in the
// user's config directory, and manages the host's TLS identity and the
// client's pinned host certificates.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/planckdotgg/planck/pkg/defaults"
)

const (
	HostConfigFile   = "host.json"
	ClientConfigFile = "client.json"
)

// The host's default ports, the first discovery.DefaultPort: below
// 47981-48500, the range Sunshine uses.
const (
	DefaultDiscoveryPort = 47700
	DefaultControlPort   = 47701
	DefaultMediaPort     = 47702
)

// Codec and chroma values use the same names as the encoder flags in the
// Phase 0 spikes, lower case.
var (
	codecs  = []string{"h264", "hevc", "av1"}
	chromas = []string{"nv12", "p010", "yuv444", "yuv444p10"}
)

// Dir is the directory that holds Planck's config files: <user config dir>/planck.
func Dir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "planck"), nil
}

// Clipboard directions a host allows, as HostConfig.Clipboard holds them.
const (
	ClipboardBoth         = "both"
	ClipboardHostToClient = "host-to-client"
	ClipboardClientToHost = "client-to-host"
	ClipboardOff          = "off"
)

var clipboards = []string{ClipboardBoth, ClipboardHostToClient, ClipboardClientToHost, ClipboardOff}

type HostConfig struct {
	HostID string `json:"hostID"`
	// Name is what clients see; empty advertises Hostname().
	Name string `json:"name"`
	// ControlPort and MediaPort 0 pick a free port; DiscoveryPort 0 turns
	// LAN discovery off.
	ControlPort   uint16 `json:"controlPort"`
	MediaPort     uint16 `json:"mediaPort"`
	DiscoveryPort uint16 `json:"discoveryPort"`
	Display       string `json:"display,omitempty"`
	// Adapter is the encoding GPU by part of its name; empty lets the host
	// choose.
	Adapter string `json:"adapter,omitempty"`
	// VDDPrimary makes a session's virtual display the primary one.
	VDDPrimary bool `json:"vddPrimary,omitempty"`
	// FixedPIN is a pairing PIN that never changes; empty rotates a random
	// one after each pairing.
	FixedPIN string `json:"fixedPIN,omitempty"`
	// NoInput ignores clients' keyboard, mouse and gamepad.
	NoInput bool `json:"noInput,omitempty"`
	// Clipboard is what the host shares: one of the Clipboard constants,
	// empty is off.
	Clipboard              string `json:"clipboard,omitempty"`
	Codec                  string `json:"codec"`
	Chroma                 string `json:"chroma"`
	FPS                    int    `json:"fps"`
	MaxBitrateKbps         int    `json:"maxBitrateKbps"`
	RequirePINForLAN       bool   `json:"requirePINForLAN"`
	AllowTailscaleSameUser bool   `json:"allowTailscaleSameUser"`
	CertPath               string `json:"certPath"`
	KeyPath                string `json:"keyPath"`
	// Policy becomes HostCapabilities.Forced: it beats the client's overrides.
	Policy defaults.UserOverrides `json:"policy"`

	// AudioDevice is the output device whose sound streams, by
	// audio.Device ID; empty is everything this machine plays.
	AudioDevice string `json:"audioDevice,omitempty"`
	// AudioExcludeApps are apps left out of the streamed sound, by name,
	// such as "Discord"; each session finds their processes.
	AudioExcludeApps AppList `json:"audioExcludeApps,omitempty"`
	// VirtualMic is where a client's microphone plays: empty finds a
	// virtual mic, VirtualMicOff refuses the microphone, and anything else
	// is an output device's ID or part of its name.
	VirtualMic string `json:"virtualMic,omitempty"`

	// StayAwake keeps this machine and its display from sleeping while
	// hosting is on.
	StayAwake bool `json:"stayAwake,omitempty"`
	// LockOnDisconnect locks this machine when a client leaves.
	LockOnDisconnect bool `json:"lockOnDisconnect,omitempty"`
	// MuteSpeakers mutes this machine's speakers while a client streams.
	MuteSpeakers bool `json:"muteSpeakers,omitempty"`
	// PrivacyMode turns this machine's own displays off while a client
	// streams from a virtual display (Windows).
	PrivacyMode bool `json:"privacyMode,omitempty"`
	// IdleTimeoutMinutes disconnects a client that sent no input for that
	// long; 0 is never.
	IdleTimeoutMinutes int `json:"idleTimeoutMinutes,omitempty"`
}

// VirtualMicOff is HostConfig.VirtualMic for no microphone.
const VirtualMicOff = "off"

// MaxIdleTimeoutMinutes is the longest IdleTimeoutMinutes, a day.
const MaxIdleTimeoutMinutes = 24 * 60

type ClientConfig struct {
	PreferredCodec  string `json:"preferredCodec"`
	PreferredChroma string `json:"preferredChroma"`
	TargetFPS       int    `json:"targetFPS"`
	MaxBitrateKbps  int    `json:"maxBitrateKbps"`
	AudioEnabled    bool   `json:"audioEnabled"`
	// ClipboardSend shares this device's clipboard with the other end of
	// a stream, and ClipboardReceive applies the other end's here. Both
	// start off.
	ClipboardSend    bool `json:"clipboardSend"`
	ClipboardReceive bool `json:"clipboardReceive"`
	// SystemKeysCapture is immersive mode: the stream takes the shortcuts
	// the client's own OS acts on, such as Cmd-Tab and the Windows key.
	SystemKeysCapture bool                  `json:"systemKeysCapture,omitempty"`
	ThemeName         string                `json:"themeName"`
	PinnedHosts       map[string]PinnedHost `json:"pinnedHosts"`
	// DeviceName is the name hosts pair this client under; empty is
	// Hostname().
	DeviceName string `json:"deviceName,omitempty"`
	// HostAliases are names the user gave hosts on this client, by host
	// ID; they replace the name the host advertises.
	HostAliases map[string]string `json:"hostAliases,omitempty"`

	Overrides     defaults.UserOverrides            `json:"overrides"`
	HostOverrides map[string]defaults.UserOverrides `json:"hostOverrides,omitempty"` // key: HostID

	// AudioVolume is the stream's playback level from 0 to 1; nil is
	// full.
	AudioVolume *float64 `json:"audioVolume,omitempty"`
	AudioMuted  bool     `json:"audioMuted,omitempty"`
	// Overlay is where the stream overlay was left; nil before the first
	// stream.
	Overlay *OverlayLayout `json:"overlay,omitempty"`
	// OpenFullscreen is whether a stream's window opens in full screen;
	// nil is on (StreamsOpenFullscreen).
	OpenFullscreen *bool `json:"openFullscreen,omitempty"`
	// OpenEveryHostDisplay opens a window for each host display, at most
	// StreamWindows of them, where the host streams several at once.
	OpenEveryHostDisplay bool `json:"openEveryHostDisplay,omitempty"`
	// MaxStreamWindows is StreamWindows as set; 0 is the default.
	MaxStreamWindows int `json:"maxStreamWindows,omitempty"`

	// Hotkeys are the hotkeys the user changed, by HotkeyAction, as
	// chords; "" is unbound. The rest keep their defaults (Hotkey).
	Hotkeys map[string]string `json:"hotkeys,omitempty"`
	// NeverSend are chords never sent to the host, such as a voice chat's
	// push-to-talk key.
	NeverSend []string `json:"neverSend,omitempty"`
	// SwapCmdCtrl trades Cmd and Ctrl in what goes to a host whose OS is
	// macOS when this one's is not, or the other way.
	SwapCmdCtrl bool `json:"swapCmdCtrl,omitempty"`
	// AudioOutputDevice and MicDevice are the devices a stream plays on
	// and records from, by audio.Device ID; empty, or a device that is
	// gone, is the system's default.
	AudioOutputDevice string `json:"audioOutputDevice,omitempty"`
	MicDevice         string `json:"micDevice,omitempty"`
	// TelemetryEnabled is anonymous usage telemetry, for the app and for
	// planck-host on this machine; nil is on (TelemetryOn).
	TelemetryEnabled *bool `json:"telemetryEnabled,omitempty"`
}

// TelemetryOn reports whether anonymous usage telemetry may send: on
// unless the user turned it off.
func (c ClientConfig) TelemetryOn() bool {
	return c.TelemetryEnabled == nil || *c.TelemetryEnabled
}

// TelemetryOn is client.json's TelemetryOn in dir, on when the file is
// missing and off when it cannot be read.
func TelemetryOn(dir string) bool {
	c, err := LoadClientConfig(dir)
	return err == nil && c.TelemetryOn()
}

// StreamsOpenFullscreen reports whether a stream's window opens in full
// screen: on unless the user turned it off.
func (c ClientConfig) StreamsOpenFullscreen() bool {
	return c.OpenFullscreen == nil || *c.OpenFullscreen
}

// The bounds and default of ClientConfig.StreamWindows.
const (
	MinStreamWindows     = 2
	MaxStreamWindows     = 4
	DefaultStreamWindows = 2
)

// StreamWindows is the most stream windows Open every host display opens:
// MaxStreamWindows as set, in bounds, or the default.
func (c ClientConfig) StreamWindows() int {
	if c.MaxStreamWindows == 0 {
		return DefaultStreamWindows
	}
	return min(max(c.MaxStreamWindows, MinStreamWindows), MaxStreamWindows)
}

// OverlayLayout places the stream overlay's button and metrics window,
// anchored to the stream view's edges or centre so that a new window size
// keeps them where they were.
type OverlayLayout struct {
	// Dock is the edge the button is docked to: "left", "right", "top",
	// "bottom", or "" while it floats at Button.
	Dock           string     `json:"dock,omitempty"`
	DockFrac       float64    `json:"dockFrac,omitempty"`
	Button         *Placement `json:"button,omitempty"`
	Metrics        bool       `json:"metrics,omitempty"`
	MetricsPlace   *Placement `json:"metricsPlace,omitempty"`
	MetricsCompact bool       `json:"metricsCompact,omitempty"`
	// ButtonX and ButtonY (the button's centre) and MetricsX and MetricsY
	// (the window's top-left corner) are fractions of the view, from
	// layouts stored before Button and MetricsPlace; the client reads them
	// only when those are missing.
	ButtonX  float64 `json:"buttonX,omitempty"`
	ButtonY  float64 `json:"buttonY,omitempty"`
	MetricsX float64 `json:"metricsX,omitempty"`
	MetricsY float64 `json:"metricsY,omitempty"`
}

// Placement is where an overlay widget sits on each axis: AnchorX and
// AnchorY are "start" (left or top), "center" or "end" (right or bottom),
// and the offsets are DIPs from that edge to the widget's same edge, or,
// signed, from the view's centre to the widget's.
type Placement struct {
	AnchorX string  `json:"anchorX"`
	AnchorY string  `json:"anchorY"`
	OffsetX float64 `json:"offsetX"`
	OffsetY float64 `json:"offsetY"`
}

// OverridesFor returns the global overrides with hostID's on top.
func (c ClientConfig) OverridesFor(hostID string) defaults.UserOverrides {
	return defaults.Merge(c.Overrides, c.HostOverrides[hostID])
}

// DefaultHostConfig returns settings for a new host in dir, with a fresh
// random HostID and the identity files beside the config.
func DefaultHostConfig(dir string) HostConfig {
	return HostConfig{
		HostID:                 newHostID(),
		Name:                   Hostname(),
		ControlPort:            DefaultControlPort,
		MediaPort:              DefaultMediaPort,
		DiscoveryPort:          DefaultDiscoveryPort,
		Codec:                  "hevc",
		Chroma:                 "nv12",
		FPS:                    120,
		MaxBitrateKbps:         100_000,
		RequirePINForLAN:       true,
		AllowTailscaleSameUser: true,
		CertPath:               filepath.Join(dir, "host-cert.pem"),
		KeyPath:                filepath.Join(dir, "host-key.pem"),
	}
}

func DefaultClientConfig() ClientConfig {
	return ClientConfig{
		PreferredCodec:  "hevc",
		PreferredChroma: "nv12",
		TargetFPS:       120,
		MaxBitrateKbps:  100_000,
		AudioEnabled:    true,
		ThemeName:       "default",
		PinnedHosts:     map[string]PinnedHost{},
	}
}

func newHostID() string {
	var b [16]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// LoadHostConfig reads <dir>/host.json. When the file does not exist it
// returns DefaultHostConfig(dir), which the caller should save so the HostID
// stays stable.
func LoadHostConfig(dir string) (HostConfig, error) {
	cfg := DefaultHostConfig(dir)
	if err := loadJSON(filepath.Join(dir, HostConfigFile), &cfg); err != nil {
		return HostConfig{}, err
	}
	return cfg, cfg.Validate()
}

func SaveHostConfig(dir string, cfg HostConfig) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	return saveJSON(filepath.Join(dir, HostConfigFile), cfg)
}

// LoadClientConfig reads <dir>/client.json, or returns defaults when it does
// not exist.
func LoadClientConfig(dir string) (ClientConfig, error) {
	cfg := DefaultClientConfig()
	var saved struct {
		Overrides json.RawMessage `json:"overrides"`
	}
	if err := loadJSON(filepath.Join(dir, ClientConfigFile), &cfg, &saved); err != nil {
		return ClientConfig{}, err
	}
	if saved.Overrides == nil {
		cfg.migrateLegacyFormat()
	}
	if cfg.PinnedHosts == nil {
		cfg.PinnedHosts = map[string]PinnedHost{}
	}
	return cfg, cfg.Validate()
}

func SaveClientConfig(dir string, cfg ClientConfig) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	return saveJSON(filepath.Join(dir, ClientConfigFile), cfg)
}

// fileMu serializes the read-modify-write of the Update functions, which
// the client's settings, its pins and a host's rename all do in one process.
var fileMu sync.Mutex

// UpdateHostConfig loads <dir>/host.json, applies fn and saves the result.
func UpdateHostConfig(dir string, fn func(*HostConfig) error) error {
	fileMu.Lock()
	defer fileMu.Unlock()
	cfg, err := LoadHostConfig(dir)
	if err != nil {
		return err
	}
	if err := fn(&cfg); err != nil {
		return err
	}
	return SaveHostConfig(dir, cfg)
}

// UpdateClientConfig loads <dir>/client.json, applies fn and saves the
// result.
func UpdateClientConfig(dir string, fn func(*ClientConfig) error) error {
	fileMu.Lock()
	defer fileMu.Unlock()
	cfg, err := LoadClientConfig(dir)
	if err != nil {
		return err
	}
	if err := fn(&cfg); err != nil {
		return err
	}
	return SaveClientConfig(dir, cfg)
}

// migrateLegacyFormat turns preferredCodec, preferredChroma and targetFPS
// into overrides where they differ from the old defaults (hevc, nv12,
// 120). It runs only for a file saved before overrides existed, so an
// override the user later resets to auto stays auto.
func (c *ClientConfig) migrateLegacyFormat() {
	o := &c.Overrides
	if c.PreferredCodec == "h264" || c.PreferredCodec == "av1" {
		o.Codec = defaults.CodecPref(c.PreferredCodec)
	}
	switch c.PreferredChroma {
	case "p010":
		o.BitDepth = "10"
	case "yuv444":
		o.Chroma = "444"
	case "yuv444p10":
		o.Chroma, o.BitDepth = "444", "10"
	}
	switch c.TargetFPS {
	case 60, 144, 240:
		o.FPS = defaults.FPSPref(strconv.Itoa(c.TargetFPS))
	}
}

func (c HostConfig) Validate() error {
	switch {
	case c.HostID == "":
		return errors.New("config: host: empty hostID")
	case c.ControlPort != 0 && c.ControlPort == c.MediaPort:
		return fmt.Errorf("config: host: controlPort and mediaPort are both %d", c.ControlPort)
	case c.FixedPIN != "" && !validPIN(c.FixedPIN):
		return errors.New("config: host: fixedPIN must be 6 digits")
	case c.Clipboard != "" && !slices.Contains(clipboards, c.Clipboard):
		return fmt.Errorf("config: host: unknown clipboard %q (want one of %v)", c.Clipboard, clipboards)
	case c.FPS <= 0 || c.MaxBitrateKbps <= 0:
		return fmt.Errorf("config: host: fps %d and maxBitrateKbps %d must be positive", c.FPS, c.MaxBitrateKbps)
	case c.CertPath == "" || c.KeyPath == "":
		return errors.New("config: host: certPath and keyPath are required")
	case c.IdleTimeoutMinutes < 0 || c.IdleTimeoutMinutes > MaxIdleTimeoutMinutes:
		return fmt.Errorf("config: host: idleTimeoutMinutes %d: want 0 to %d", c.IdleTimeoutMinutes, MaxIdleTimeoutMinutes)
	}
	if err := c.Policy.Validate(); err != nil {
		return fmt.Errorf("config: host: policy: %w", err)
	}
	return validateFormat("host", c.Codec, c.Chroma)
}

func (c ClientConfig) Validate() error {
	if c.TargetFPS <= 0 || c.MaxBitrateKbps <= 0 {
		return fmt.Errorf("config: client: targetFPS %d and maxBitrateKbps %d must be positive", c.TargetFPS, c.MaxBitrateKbps)
	}
	if err := c.Overrides.Validate(); err != nil {
		return fmt.Errorf("config: client: overrides: %w", err)
	}
	for _, id := range slices.Sorted(maps.Keys(c.HostOverrides)) {
		if err := c.HostOverrides[id].Validate(); err != nil {
			return fmt.Errorf("config: client: host %s overrides: %w", id, err)
		}
	}
	if err := c.validateHotkeys(); err != nil {
		return err
	}
	return validateFormat("client", c.PreferredCodec, c.PreferredChroma)
}

// validPIN reports whether pin is a pairing PIN: six ASCII digits.
func validPIN(pin string) bool {
	return len(pin) == 6 && strings.Trim(pin, "0123456789") == ""
}

func validateFormat(who, codec, chroma string) error {
	if !slices.Contains(codecs, codec) {
		return fmt.Errorf("config: %s: unknown codec %q (want one of %v)", who, codec, codecs)
	}
	if !slices.Contains(chromas, chroma) {
		return fmt.Errorf("config: %s: unknown chroma %q (want one of %v)", who, chroma, chromas)
	}
	return nil
}

func loadJSON(path string, vs ...any) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, v := range vs {
		if err := json.Unmarshal(b, v); err != nil {
			return fmt.Errorf("config: %s: %w", path, err)
		}
	}
	return nil
}

// saveJSON writes through a temp file and rename so a crash never leaves a
// half-written config behind.
func saveJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(b, '\n'), 0o600)
}

func writeFileAtomic(path string, data []byte, perm fs.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(perm); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// AppList is a list of app names. It is a string inside so that
// HostConfig stays comparable; in JSON it is an array.
type AppList string

// NewAppList keeps the names that are not blank, once each, trimmed.
func NewAppList(names ...string) AppList {
	var kept []string
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n != "" && !slices.ContainsFunc(kept, func(k string) bool { return strings.EqualFold(k, n) }) {
			kept = append(kept, n)
		}
	}
	return AppList(strings.Join(kept, "\n"))
}

// Names are the apps in the list.
func (l AppList) Names() []string {
	if l == "" {
		return nil
	}
	return strings.Split(string(l), "\n")
}

func (l AppList) MarshalJSON() ([]byte, error) { return json.Marshal(l.Names()) }

func (l *AppList) UnmarshalJSON(b []byte) error {
	var names []string
	if err := json.Unmarshal(b, &names); err != nil {
		return err
	}
	*l = NewAppList(names...)
	return nil
}
