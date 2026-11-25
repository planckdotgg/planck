package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"

	"github.com/planckdotgg/planck/internal/session"
	"github.com/planckdotgg/planck/internal/ui"
	"github.com/planckdotgg/planck/pkg/config"
	"github.com/planckdotgg/planck/pkg/discovery"
)

// measureFrames is how many frames MeasureEncoder times, a second at
// 120 fps after the warm-up.
const measureFrames = 120

// appHostConfig is the host the app's Hosting toggle starts: host.json's
// hosting settings in dir, whose policy NewHost applies, injecting input
// unless they forbid it.
func appHostConfig(dir string, logf func(string, ...any)) func(ui.Settings) session.HostConfig {
	return func(s ui.Settings) session.HostConfig {
		c := session.DefaultHostConfig()
		c.ConfigDir, c.Adapter, c.InjectInput, c.Logf = dir, s.Encoder, true, logf
		stored, err := loadHostConfig(dir)
		if err != nil {
			// NewHost reads host.json too, and fails with the error.
			return c
		}
		return c.FromStored(stored)
	}
}

func loadHostConfig(dir string) (config.HostConfig, error) {
	if dir == "" {
		d, err := config.Dir()
		if err != nil {
			return config.HostConfig{}, err
		}
		dir = d
	}
	return config.LoadHostConfig(dir)
}

func hostSettingsFrom(c config.HostConfig) ui.HostSettings {
	return ui.HostSettings{
		Name: c.Name, Encoder: c.Adapter, Display: c.Display, Policy: c.Policy.Normalize(), VDDPrimary: c.VDDPrimary,
		FixedPIN: c.FixedPIN, RequirePINForLAN: c.RequirePINForLAN, AllowTailscaleSameUser: c.AllowTailscaleSameUser,
		AllowInput: !c.NoInput, Clipboard: ui.ClipboardDirection(cmp.Or(c.Clipboard, config.ClipboardOff)),
		Ports: ui.HostPorts{Control: int(c.ControlPort), Media: int(c.MediaPort), Discovery: int(c.DiscoveryPort)},
	}
}

// applyHostSettings writes s over c; Validate checks the rest.
func applyHostSettings(c *config.HostConfig, s ui.HostSettings) error {
	for _, p := range []int{s.Ports.Control, s.Ports.Media, s.Ports.Discovery} {
		if p < 0 || p > 65535 {
			return fmt.Errorf("port %d: want 0 to 65535", p)
		}
	}
	if len(s.Name) > session.MaxHostNameLen {
		return fmt.Errorf("a host name is at most %d bytes", session.MaxHostNameLen)
	}
	policy := s.Policy.Normalize()
	if err := policy.Validate(); err != nil {
		return err
	}
	c.Name, c.Adapter, c.Display, c.Policy, c.VDDPrimary = s.Name, s.Encoder, s.Display, policy, s.VDDPrimary
	c.FixedPIN, c.RequirePINForLAN, c.AllowTailscaleSameUser = s.FixedPIN, s.RequirePINForLAN, s.AllowTailscaleSameUser
	c.NoInput = !s.AllowInput
	c.Clipboard = string(s.Clipboard)
	if s.Clipboard == ui.ClipboardOff {
		c.Clipboard = ""
	}
	c.ControlPort, c.MediaPort, c.DiscoveryPort = uint16(s.Ports.Control), uint16(s.Ports.Media), uint16(s.Ports.Discovery)
	return nil
}

func (b *backend) HostSettings() ui.HostSettings {
	b.mu.Lock()
	defer b.mu.Unlock()
	return hostSettingsFrom(b.hostStored)
}

// SetHostSettings keeps s in host.json. While hosting, a new name goes to
// clients at once, and any other change restarts hosting with it.
func (b *backend) SetHostSettings(s ui.HostSettings) error {
	s.Name, s.FixedPIN = strings.TrimSpace(s.Name), strings.TrimSpace(s.FixedPIN)
	var stored config.HostConfig
	err := config.UpdateHostConfig(b.dir, func(c *config.HostConfig) error {
		if err := applyHostSettings(c, s); err != nil {
			return err
		}
		stored = *c
		return nil
	})
	if err != nil {
		return err
	}
	b.mu.Lock()
	before := hostSettingsFrom(b.hostStored)
	b.hostStored = stored
	b.settings.HostName, b.settings.Encoder = stored.Name, stored.Adapter
	h := b.host
	b.mu.Unlock()
	b.changed()
	if h == nil {
		return nil
	}
	after := hostSettingsFrom(stored)
	if before.Name != after.Name {
		if err := h.SetName(after.Name); err != nil {
			return err
		}
	}
	before.Name, after.Name = "", ""
	if before != after {
		return b.SetHosting(context.Background(), true)
	}
	return nil
}

func (b *backend) Displays() []ui.DisplayInfo {
	var out []ui.DisplayInfo
	for _, d := range b.displays() {
		out = append(out, ui.DisplayInfo{ID: d.ID, Width: d.Width, Height: d.Height, RefreshHz: d.RefreshHz, Primary: d.Primary})
	}
	return out
}

// MeasureEncoder times gpu's encoder and keeps the p50 for Encoders.
func (b *backend) MeasureEncoder(ctx context.Context, gpu string) (float64, error) {
	b.mu.Lock()
	known := slices.ContainsFunc(b.encoders, func(e ui.EncoderInfo) bool { return e.GPU == gpu })
	b.mu.Unlock()
	if !known {
		return 0, fmt.Errorf("no encoder on %q", gpu)
	}
	l, err := b.measure(ctx, gpu)
	if err != nil {
		return 0, err
	}
	b.mu.Lock()
	for i := range b.encoders {
		if b.encoders[i].GPU == gpu {
			b.encoders[i].EncodeMs = l.P50
		}
	}
	b.mu.Unlock()
	b.changed()
	return l.P50, nil
}

// TestPorts binds each port as hosting would, then lets it go. A port
// hosting holds now, and 0, pass.
func (b *backend) TestPorts(p ui.HostPorts) ui.PortStatus {
	using := b.Hosting().Using
	check := func(port, held int, open func(string) (interface{ Close() error }, error)) string {
		if port == 0 || port == held {
			return ""
		}
		if port < 0 || port > 65535 {
			return "Ports go from 1 to 65535."
		}
		c, err := open(fmt.Sprintf(":%d", port))
		if err != nil {
			return hostingError(err)
		}
		c.Close()
		return ""
	}
	udp := func(addr string) (interface{ Close() error }, error) { return net.ListenPacket("udp", addr) }
	shared := func(addr string) (interface{ Close() error }, error) { return discovery.OpenPort(addr) }
	st := ui.PortStatus{
		Control:   check(p.Control, using.Control, udp),
		Media:     check(p.Media, using.Media, udp),
		Discovery: check(p.Discovery, using.Discovery, shared),
	}
	if p.Control != 0 && p.Control == p.Media {
		st.Media = "Control and media need different ports."
	}
	return st
}

// PairedClients lists the devices that may stream from this machine:
// the running host's, or paired-clients.json's while not hosting.
func (b *backend) PairedClients() []ui.PairedClient {
	b.mu.Lock()
	h := b.host
	b.mu.Unlock()
	var all map[string]session.PairedClient
	var connected string
	if h != nil {
		all, connected = h.PairedClients(), h.ConnectedClient()
	} else {
		var err error
		if all, err = session.LoadPairedClients(b.dir); err != nil {
			b.logf("planck: paired clients: %v", err)
		}
	}
	out := make([]ui.PairedClient, 0, len(all))
	for fp, c := range all {
		out = append(out, ui.PairedClient{Name: c.Name, Fingerprint: fp, PairedAt: c.PairedAt, LastSeen: c.LastSeen,
			TrustedViaTailnet: c.TrustedViaTailnet(), Connected: fp == connected})
	}
	slices.SortFunc(out, func(x, y ui.PairedClient) int {
		return cmp.Or(y.PairedAt.Compare(x.PairedAt), strings.Compare(x.Fingerprint, y.Fingerprint))
	})
	return out
}

func (b *backend) RevokeClient(fingerprint string) error { return b.revoke([]string{fingerprint}) }

func (b *backend) RevokeAllClients() error { return b.revoke(nil) }

func (b *backend) revoke(fps []string) error {
	b.mu.Lock()
	h := b.host
	b.mu.Unlock()
	var err error
	switch {
	case h != nil && fps == nil:
		err = h.RevokeAllClients()
	case h != nil:
		err = h.RevokeClient(fps[0])
	default:
		err = session.RevokePairedClients(b.dir, fps)
	}
	if errors.Is(err, session.ErrNotPaired) {
		return errors.New("that device is not paired")
	}
	b.changed()
	return err
}

func (b *backend) ClientName() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.name
}

// SetClientName keeps name in client.json for the next connect; empty
// goes back to the machine's name.
func (b *backend) SetClientName(name string) error {
	name = strings.TrimSpace(name)
	if len(name) > session.MaxHostNameLen {
		return fmt.Errorf("a device name is at most %d bytes", session.MaxHostNameLen)
	}
	err := config.UpdateClientConfig(b.dir, func(c *config.ClientConfig) error {
		c.DeviceName = name
		return nil
	})
	if err != nil {
		return err
	}
	b.mu.Lock()
	b.name = cmp.Or(name, config.Hostname())
	b.mu.Unlock()
	b.changed()
	return nil
}
