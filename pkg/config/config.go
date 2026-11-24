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
	"sync"

	"github.com/planckdotgg/planck/pkg/defaults"
)

const (
	HostConfigFile   = "host.json"
	ClientConfigFile = "client.json"
)

// The host's default ports, with discovery.DefaultPort (47700) beside
// them: below 47981-48500, the range Sunshine uses.
const (
	DefaultControlPort = 47701
	DefaultMediaPort   = 47702
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

type HostConfig struct {
	HostID string `json:"hostID"`
	// Name is what clients see; empty advertises Hostname().
	Name                   string `json:"name"`
	ControlPort            uint16 `json:"controlPort"`
	MediaPort              uint16 `json:"mediaPort"`
	Display                string `json:"display,omitempty"`
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
}

type ClientConfig struct {
	PreferredCodec  string                `json:"preferredCodec"`
	PreferredChroma string                `json:"preferredChroma"`
	TargetFPS       int                   `json:"targetFPS"`
	MaxBitrateKbps  int                   `json:"maxBitrateKbps"`
	AudioEnabled    bool                  `json:"audioEnabled"`
	ThemeName       string                `json:"themeName"`
	PinnedHosts     map[string]PinnedHost `json:"pinnedHosts"`
	// HostAliases are names the user gave hosts on this client, by host
	// ID; they replace the name the host advertises.
	HostAliases map[string]string `json:"hostAliases,omitempty"`

	Overrides     defaults.UserOverrides            `json:"overrides"`
	HostOverrides map[string]defaults.UserOverrides `json:"hostOverrides,omitempty"` // key: HostID
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
	case c.ControlPort == 0 || c.MediaPort == 0 || c.ControlPort == c.MediaPort:
		return fmt.Errorf("config: host: controlPort %d and mediaPort %d must be distinct and non-zero", c.ControlPort, c.MediaPort)
	case c.FPS <= 0 || c.MaxBitrateKbps <= 0:
		return fmt.Errorf("config: host: fps %d and maxBitrateKbps %d must be positive", c.FPS, c.MaxBitrateKbps)
	case c.CertPath == "" || c.KeyPath == "":
		return errors.New("config: host: certPath and keyPath are required")
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
	return validateFormat("client", c.PreferredCodec, c.PreferredChroma)
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
