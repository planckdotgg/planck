package calibrate

import (
	"context"
	"sync"

	"github.com/planckdotgg/planck/internal/session"
	"github.com/planckdotgg/planck/pkg/defaults"
)

// SessionDialer dials each calibration session as a session.Client to
// Target, with Config's identity, route and capabilities. A session
// carries no audio, clipboard, microphone or gamepads; a network one
// opens no decoder, and a display one shows in DisplaySink.
type SessionDialer struct {
	Config session.ClientConfig
	Target session.Target
	// DisplaySink opens the window that Phase 3 measures; nil leaves
	// display sessions on Config's sink.
	DisplaySink session.SinkOpener

	mu sync.Mutex
}

func (d *SessionDialer) Dial(ctx context.Context, mode session.CalibrationMode, o defaults.UserOverrides, display bool) (Conn, error) {
	d.mu.Lock()
	cfg := d.Config
	d.mu.Unlock()
	cfg.Calibrate, cfg.Overrides, cfg.NoAudio = mode, o, true
	cfg.Clipboard, cfg.OpenMic, cfg.Gamepads, cfg.OnRumble = session.ClipboardConfig{}, nil, nil, nil
	switch {
	case mode == session.CalibrateNetwork:
		cfg.OpenSink = session.NullSinkOpener(&session.NullSink{})
	case display && d.DisplaySink != nil:
		cfg.OpenSink = d.DisplaySink
	}
	c, err := session.NewClient(cfg)
	if err != nil {
		return nil, err
	}
	if err := c.Connect(ctx, d.Target); err != nil {
		c.Close()
		return nil, err
	}
	if cfg.PIN != "" {
		// The first session paired; the host rotates its PIN after it, and
		// the pin made carries the rest.
		d.mu.Lock()
		d.Config.PIN = ""
		d.mu.Unlock()
	}
	return c, nil
}
