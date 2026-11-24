// Command planck-host streams this machine's display, audio and input to a
// Planck client. With -synth it streams a generated pattern instead, for
// benchmarks on a chosen GPU without a monitor.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/planckdotgg/planck/internal/session"
	"github.com/planckdotgg/planck/pkg/discovery"
	"github.com/planckdotgg/planck/pkg/encode"
)

func main() {
	cfg, statsEvery, err := parseConfig(os.Args[1:], os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "planck-host:", err)
		os.Exit(2)
	}
	if err := run(cfg, statsEvery); err != nil {
		fmt.Fprintln(os.Stderr, "planck-host:", err)
		os.Exit(1)
	}
}

func parseConfig(args []string, out io.Writer) (session.HostConfig, time.Duration, error) {
	cfg := session.DefaultHostConfig()
	fs := flag.NewFlagSet("planck-host", flag.ContinueOnError)
	fs.SetOutput(out)
	var (
		listen        = fs.String("listen", cfg.ListenAddr, "QUIC control address")
		mediaPort     = fs.Int("media-port", cfg.MediaPort, "UDP media port (0 picks a free one)")
		discoveryPort = fs.Int("discovery-port", discovery.DefaultPort, "LAN discovery port (0 disables discovery)")
		codec         = fs.String("codec", "hevc", "video codec: hevc, av1 or h264")
		chroma        = fs.String("chroma", "nv12", "coded chroma: nv12, p010, yuv444 or yuv444p10")
		noInput       = fs.Bool("no-input", false, "ignore client keyboard, mouse and gamepad input")
		stats         = fs.Duration("stats", 2*time.Second, "print stats this often while a client is connected (0 disables)")
		split         = fs.String("split", "auto", "split-frame encode across the GPU's engines: auto (1440p and up, or 1080p from 200 fps, on LAN, without -slices), on or off")
	)
	fs.StringVar(&cfg.ConfigDir, "config", "", "config directory (default: the user config dir /planck)")
	fs.StringVar(&cfg.Name, "name", "", "host name shown to clients (default: the machine name)")
	fs.StringVar(&cfg.Display, "display", "", "display ID to capture (default: the primary display)")
	fs.StringVar(&cfg.Adapter, "adapter", "", `encoding GPU, by name or vendor ("NVIDIA", "AMD", "Intel"); default: the display's GPU`)
	fs.IntVar(&cfg.FPS, "fps", 0, "frame rate (default: the display refresh, or 60 with -synth)")
	fs.IntVar(&cfg.BitrateKbps, "bitrate", cfg.BitrateKbps, "maximum video bitrate in kbps")
	fs.IntVar(&cfg.Width, "width", cfg.Width, "synthetic frame width (-synth)")
	fs.IntVar(&cfg.Height, "height", cfg.Height, "synthetic frame height (-synth)")
	fs.IntVar(&cfg.Slices, "slices", cfg.Slices, "FEC slices per frame (1-16)")
	fs.BoolVar(&cfg.SubFrame, "subframe", false, "send slices as the encoder finishes them (needs -slices >= 2)")
	fs.StringVar(&cfg.PIN, "pin", "", "fixed 6-digit pairing PIN (default: a random one that changes after each pairing)")
	fs.BoolVar(&cfg.NoAudio, "no-audio", false, "do not capture or stream audio")
	fs.BoolVar(&cfg.Synth, "synth", false, "stream a generated test pattern at -width x -height and -fps instead of a display")
	if err := fs.Parse(args); err != nil {
		return cfg, 0, err
	}
	if fs.NArg() > 0 {
		return cfg, 0, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	var err error
	if cfg.Codec, err = session.ParseCodec(*codec); err != nil {
		return cfg, 0, err
	}
	if cfg.Chroma, err = session.ParseChroma(*chroma); err != nil {
		return cfg, 0, err
	}
	switch *split {
	case "auto":
		cfg.Split = encode.SplitAuto
	case "on":
		cfg.Split = encode.SplitOn
	case "off":
		cfg.Split = encode.SplitOff
	default:
		return cfg, 0, fmt.Errorf("-split %q: want auto, on or off", *split)
	}
	cfg.ListenAddr, cfg.MediaPort = *listen, *mediaPort
	cfg.DiscoveryAddr = "off"
	if *discoveryPort > 0 {
		cfg.DiscoveryAddr = fmt.Sprintf(":%d", *discoveryPort)
	}
	cfg.InjectInput = !*noInput
	switch {
	case cfg.PIN != "" && (len(cfg.PIN) != 6 || strings.Trim(cfg.PIN, "0123456789") != ""):
		return cfg, 0, fmt.Errorf("-pin %q: want 6 digits", cfg.PIN)
	case cfg.Slices < 1 || cfg.Slices > 16:
		return cfg, 0, fmt.Errorf("-slices %d: want 1 to 16", cfg.Slices)
	case cfg.SubFrame && cfg.Slices < 2:
		return cfg, 0, errors.New("-subframe needs -slices 2 or more")
	case cfg.BitrateKbps <= 0 || cfg.FPS < 0:
		return cfg, 0, errors.New("-bitrate must be positive and -fps not negative")
	case cfg.Synth && (cfg.Width <= 0 || cfg.Height <= 0):
		return cfg, 0, errors.New("-synth needs a positive -width and -height")
	case *mediaPort < 0 || *mediaPort > 65535:
		return cfg, 0, fmt.Errorf("-media-port %d out of range", *mediaPort)
	}
	if cfg.Synth && cfg.FPS == 0 {
		cfg.FPS = 60
	}
	return cfg, *stats, nil
}

func run(cfg session.HostConfig, statsEvery time.Duration) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cfg.Logf = log.Printf
	h, err := session.NewHost(cfg)
	if err != nil {
		return err
	}
	if err := h.Start(ctx); err != nil {
		return err
	}
	defer h.Stop()

	source := "display"
	if cfg.Synth {
		source = fmt.Sprintf("synthetic %dx%d@%d", cfg.Width, cfg.Height, cfg.FPS)
	}
	fmt.Printf("Planck host %q (%s)\n", h.Name(), h.HostID())
	fmt.Printf("  listening    %v (media port %d, discovery %s)\n", h.Addr(), cfg.MediaPort, cfg.DiscoveryAddr)
	fmt.Printf("  source       %s, %s %s, up to %d kbps\n", source, cfg.Codec, cfg.Chroma, cfg.BitrateKbps)
	fmt.Printf("  fingerprint  %s\n", h.Fingerprint())
	fmt.Printf("  pairing PIN  %s\n", h.PIN())
	if !cfg.InjectInput {
		fmt.Println("  input        ignored (-no-input)")
	}

	var tick <-chan time.Time
	if statsEvery > 0 {
		t := time.NewTicker(statsEvery)
		defer t.Stop()
		tick = t.C
	}
	pin := h.PIN()
	for {
		select {
		case <-ctx.Done():
			fmt.Println("\nplanck-host: shutting down")
			return nil
		case <-tick:
			if p := h.PIN(); p != pin {
				pin = p
				fmt.Printf("pairing PIN is now %s\n", pin)
			}
			if s := h.Stats(); s.Connected {
				fmt.Println(statsLine(s))
			}
		}
	}
}

func statsLine(s session.HostStats) string {
	return fmt.Sprintf("%s %s | %dx%d %.0f fps | encode p50 %.2f p99 %.2f ms | %.0f/%d kbps | rtt %.2f ms | fec loss %.1f%% parity %.0f%% | client loss %.1f%% decode %.2f ms | gpu %s",
		s.Client, s.Path, s.Video.Width, s.Video.Height, s.FPS, s.EncodeMs.P50, s.EncodeMs.P99, s.SentKbps, s.TargetKbps,
		s.RTTMs, s.FECLossPct, s.FECParityPct, float64(s.Report.LossPermille)/10, float64(s.Report.DecodeMicros)/1000, s.Video.Power)
}
