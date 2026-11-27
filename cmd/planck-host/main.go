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
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/planckdotgg/planck/internal/session"
	"github.com/planckdotgg/planck/internal/telemetry"
	"github.com/planckdotgg/planck/internal/version"
	"github.com/planckdotgg/planck/pkg/config"
	"github.com/planckdotgg/planck/pkg/defaults"
	"github.com/planckdotgg/planck/pkg/discovery"
	"github.com/planckdotgg/planck/pkg/encode"
	"github.com/planckdotgg/planck/pkg/inputtrace"
)

func main() {
	o, err := parseOptions(os.Args[1:], os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if errors.Is(err, errShowVersion) {
		fmt.Println("planck-host", version.Full())
		return
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "planck-host:", err)
		os.Exit(2)
	}
	if err := run(o); err != nil {
		fmt.Fprintln(os.Stderr, "planck-host:", err)
		os.Exit(1)
	}
}

var errShowVersion = errors.New("-version")

type options struct {
	cfg        session.HostConfig
	statsEvery time.Duration
	// setName, when set, renames the host in host.json before it starts.
	setName *string
	// noTelemetry turns anonymous usage telemetry off for this run.
	noTelemetry bool
}

func parseConfig(args []string, out io.Writer) (session.HostConfig, time.Duration, error) {
	o, err := parseOptions(args, out)
	return o.cfg, o.statsEvery, err
}

func parseOptions(args []string, out io.Writer) (options, error) {
	var o options
	cfg := session.DefaultHostConfig()
	fs := flag.NewFlagSet("planck-host", flag.ContinueOnError)
	fs.SetOutput(out)
	var (
		listen        = fs.String("listen", cfg.ListenAddr, "QUIC control address")
		mediaPort     = fs.Int("media-port", cfg.MediaPort, "UDP media port (0 picks a free one)")
		discoveryPort = fs.Int("discovery-port", discovery.DefaultPort, "LAN discovery port (0 disables discovery)")
		codec         = fs.String("codec", "hevc", "video codec: auto, pyrowave, hevc, av1 or h264; given, it also binds clients that resolve the stream")
		chroma        = fs.String("chroma", "nv12", "coded chroma: auto, nv12, p010, yuv444 or yuv444p10 (or 420, 444); given, it also binds resolving clients")
		noInput       = fs.Bool("no-input", false, "ignore client keyboard, mouse and gamepad input")
		stats         = fs.Duration("stats", 2*time.Second, "print stats this often while a client is connected (0 disables)")
		split         = fs.String("split", "auto", "split-frame encode across the GPU's engines: auto (1440p and up, or 1080p from 200 fps, on LAN, without -slices), on or off")
		bitDepth      = fs.String("bit-depth", "auto", "policy: bit depth auto, 8 or 10")
		res           = fs.String("res", "auto", "policy: stream size auto, 1080p, 1440p, 4k or WxH (needs a virtual display)")
		bitrateMbps   = fs.Int("bitrate-mbps", 0, "policy: bitrate in Mbps (0: auto)")
		vddMode       = fs.String("vdd", "auto", "policy: SudoVDA virtual display at the client's size: auto, on or off")
		vddGPU        = fs.String("vdd-gpu", "", `policy: GPU that renders the virtual display and encodes, by name ("4080", "Radeon"); default -adapter`)
		subSlices     = fs.String("subframe-slices", "auto", "policy: codec slices sent as they finish: auto, 1, 4 or 8")
	)
	fs.BoolVar(&cfg.VDDPrimary, "vdd-primary", false, "make the virtual display the primary one, so games open on it")
	fs.StringVar(&cfg.ConfigDir, "config", "", "config directory (default: the user config dir /planck)")
	fs.StringVar(&cfg.Name, "name", "", "host name shown to clients for this run only (default: host.json's name, or the machine name)")
	setName := fs.String("set-name", "", `rename the host for good: keep the name in host.json and start under it ("" goes back to the machine name)`)
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
	excludePIDs := fs.String("audio-exclude-pids", "", "comma-separated processes, such as a voice chat app, left out of the streamed audio (macOS; Windows leaves out only Planck)")
	fs.StringVar(&cfg.MicDevice, "mic-device", "", `virtual input device the client's microphone plays into, by part of its name (default: Planck Virtual Audio, then Steam Streaming Microphone, VB-Cable or BlackHole); "off" refuses the microphone`)
	clip := fs.String("clipboard", "both", "share the clipboard: both, host-to-client, client-to-host or off")
	fs.BoolVar(&cfg.Synth, "synth", false, "stream a generated test pattern at -width x -height and -fps instead of a display")
	fs.BoolVar(&cfg.InputTrace, "input-trace", false, "trace input and the cursor to the config directory's logs/input-trace-host-*.jsonl for every session (also "+inputtrace.Env+"=1); without it the host traces while a client asks")
	fs.BoolVar(&o.noTelemetry, "no-telemetry", false, "send no anonymous usage telemetry this run (Settings › General, or \"telemetryEnabled\": false in client.json, turns it off for good)")
	showVersion := fs.Bool("version", false, "print the version and exit")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if *showVersion {
		return o, errShowVersion
	}
	if fs.NArg() > 0 {
		return o, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "set-name" {
			o.setName = setName
		}
	})
	if o.setName != nil && cfg.Name != "" {
		return o, errors.New("-name and -set-name exclude each other")
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	var err error
	if *codec != "auto" {
		if cfg.Codec, err = session.ParseCodec(*codec); err != nil {
			return o, err
		}
		if cfg.Codec == encode.PyroWave {
			cfg.Codec = 0
		}
	}
	if *chroma != "auto" {
		if cfg.Chroma, err = session.ParseChroma(*chroma); err != nil {
			return o, err
		}
	}
	if cfg.Policy, err = hostPolicy(set, *codec, *chroma, *bitDepth, *res, *vddMode, *vddGPU, *subSlices, *split, cfg, *bitrateMbps); err != nil {
		return o, err
	}
	switch *split {
	case "auto":
		cfg.Split = encode.SplitAuto
	case "on":
		cfg.Split = encode.SplitOn
	case "off":
		cfg.Split = encode.SplitOff
	default:
		return o, fmt.Errorf("-split %q: want auto, on or off", *split)
	}
	cfg.ListenAddr, cfg.MediaPort = *listen, *mediaPort
	cfg.DiscoveryAddr = "off"
	if *discoveryPort > 0 {
		cfg.DiscoveryAddr = fmt.Sprintf(":%d", *discoveryPort)
	}
	cfg.InjectInput = !*noInput
	cfg.InputTrace = cfg.InputTrace || inputtrace.EnvOn()
	if cfg.MicDevice == "off" {
		cfg.MicDevice, cfg.NoMic = "", true
	}
	switch *clip {
	case "both":
		cfg.Clipboard = session.ClipboardConfig{Send: true, Receive: true}
	case "host-to-client":
		cfg.Clipboard = session.ClipboardConfig{Send: true}
	case "client-to-host":
		cfg.Clipboard = session.ClipboardConfig{Receive: true}
	case "off":
	default:
		return o, fmt.Errorf("-clipboard %q: want both, host-to-client, client-to-host or off", *clip)
	}
	for _, f := range strings.FieldsFunc(*excludePIDs, func(r rune) bool { return r == ',' || r == ' ' }) {
		pid, err := strconv.Atoi(f)
		if err != nil || pid <= 0 {
			return o, fmt.Errorf("-audio-exclude-pids: %q is not a process ID", f)
		}
		cfg.AudioExcludePIDs = append(cfg.AudioExcludePIDs, pid)
	}
	switch {
	case cfg.PIN != "" && (len(cfg.PIN) != 6 || strings.Trim(cfg.PIN, "0123456789") != ""):
		return o, fmt.Errorf("-pin %q: want 6 digits", cfg.PIN)
	case cfg.Slices < 1 || cfg.Slices > 16:
		return o, fmt.Errorf("-slices %d: want 1 to 16", cfg.Slices)
	case cfg.SubFrame && cfg.Slices < 2:
		return o, errors.New("-subframe needs -slices 2 or more")
	case cfg.BitrateKbps <= 0 || cfg.FPS < 0:
		return o, errors.New("-bitrate must be positive and -fps not negative")
	case cfg.Synth && (cfg.Width <= 0 || cfg.Height <= 0):
		return o, errors.New("-synth needs a positive -width and -height")
	case *mediaPort < 0 || *mediaPort > 65535:
		return o, fmt.Errorf("-media-port %d out of range", *mediaPort)
	}
	if cfg.Synth && cfg.FPS == 0 {
		cfg.FPS = 60
	}
	o.cfg, o.statsEvery = cfg, *stats
	return o, nil
}

// hostPolicy is the overrides the flags the user gave bind resolving
// clients to. Flags left at their defaults bind nothing: their defaults
// are for older clients.
func hostPolicy(set map[string]bool, codec, chroma, bitDepth, res, vddMode, vddGPU, subSlices, split string, cfg session.HostConfig, bitrateMbps int) (defaults.UserOverrides, error) {
	k := session.Knobs{BitDepth: bitDepth, Resolution: res, VDD: vddMode, VDDGPU: vddGPU, Slices: subSlices, BitrateMbps: bitrateMbps}
	if set["codec"] {
		k.Codec = codec
	}
	if set["chroma"] {
		k.Chroma = chroma
	}
	if set["split"] {
		k.Split = split
	}
	if k.VDDGPU == "" {
		k.VDDGPU = cfg.Adapter
	}
	if set["fps"] && slices.Contains([]int{60, 120, 144, 240}, cfg.FPS) {
		k.FPS = strconv.Itoa(cfg.FPS)
	}
	if set["slices"] && subSlices == "auto" && slices.Contains([]int{1, 4, 8}, cfg.Slices) {
		k.Slices = strconv.Itoa(cfg.Slices)
	}
	if set["bitrate"] && k.BitrateMbps == 0 {
		k.BitrateMbps = max(1, cfg.BitrateKbps/1000)
	}
	o, err := k.Overrides()
	if err != nil {
		return o, fmt.Errorf("policy: %w", err)
	}
	return o, nil
}

func run(o options) error {
	cfg, statsEvery := o.cfg, o.statsEvery
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cfg.Logf = log.Printf
	tel, telOn := hostTelemetry(cfg.ConfigDir, o.noTelemetry)
	defer tel.Close(2 * time.Second)
	cfg.OnSession = sessionTelemetry(tel, telOn)
	h, err := session.NewHost(cfg)
	if err != nil {
		return err
	}
	// Stop removes any virtual display: on return, on a signal, and on a
	// panic of this goroutine. If the process dies another way, SudoVDA's
	// watchdog removes it once the pings stop.
	defer h.Stop()
	if o.setName != nil {
		if err := h.SetName(*o.setName); err != nil {
			return err
		}
	}
	if err := h.Start(ctx); err != nil {
		return err
	}
	tel.Launched()

	source := "display"
	if cfg.Synth {
		source = fmt.Sprintf("synthetic %dx%d@%d", cfg.Width, cfg.Height, cfg.FPS)
	}
	fmt.Printf("Planck host %q (%s)\n", h.Name(), h.HostID())
	fmt.Printf("  listening    %v (media port %d, discovery %s)\n", h.Addr(), cfg.MediaPort, cfg.DiscoveryAddr)
	fmt.Printf("  source       %s, %s %s, up to %d kbps\n", source, cfg.Codec, cfg.Chroma, cfg.BitrateKbps)
	fmt.Printf("  policy       %s\n", policyLine(cfg.Policy))
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
		case <-h.Done():
			return h.Err()
		case <-tick:
			if p := h.PIN(); p != pin {
				pin = p
				fmt.Printf("pairing PIN is now %s\n", pin)
			}
			if s := h.Stats(); s.Connected {
				fmt.Println(statsLine(s) + duplexLine(s))
			}
		}
	}
}

// policyLine lists the knobs the host binds, or says it binds none.
func policyLine(o defaults.UserOverrides) string {
	var parts []string
	add := func(k, v string) {
		if v != "" && v != defaults.Auto {
			parts = append(parts, k+"="+v)
		}
	}
	add("codec", string(o.Codec))
	add("chroma", string(o.Chroma))
	add("bit-depth", string(o.BitDepth))
	add("res", string(o.Resolution))
	add("fps", string(o.FPS))
	if o.BitrateMbps > 0 {
		add("bitrate-mbps", strconv.Itoa(o.BitrateMbps))
	}
	add("vdd", string(o.VirtualDisplay))
	add("vdd-gpu", o.VirtualDisplayGPU)
	add("slices", string(o.SubFrameSlices))
	add("nvenc-split", string(o.NVENC2EngineSplit))
	if len(parts) == 0 {
		return "auto"
	}
	return strings.Join(parts, " ")
}

// duplexLine reports the client's microphone and the clipboard counts,
// never what the clipboard holds.
func duplexLine(s session.HostStats) string {
	mic := s.Mic.Hint
	if s.Mic.Device != "" {
		mic = fmt.Sprintf("%s, %d packets, %d concealed", s.Mic.Device, s.Mic.Packets, s.Mic.Concealed)
	}
	return fmt.Sprintf(" | mic %s | clipboard sent %d received %d skipped %d", mic, s.Clipboard.Sent, s.Clipboard.Received, s.Clipboard.Skipped)
}

func statsLine(s session.HostStats) string {
	if p := s.Profile; p != nil {
		vd := "physical"
		if s.VirtualDisplay != "" {
			vd = "virtual " + s.VirtualDisplay
		}
		return fmt.Sprintf("%s %s | %s %d-bit %dx%d %.0f fps on %s (%s, %s) | encode p50 %.2f p99 %.2f ms | %.0f/%d kbps | rtt %.2f ms | fec loss %.1f%% parity %.0f%% | client loss %.1f%% decode %.2f ms | gpu %s",
			s.Client, s.Path, p.Codec, p.BitDepth, s.Video.Width, s.Video.Height, s.FPS, p.EncodeAdapter.Name, p.EncoderAPI, vd,
			s.EncodeMs.P50, s.EncodeMs.P99, s.SentKbps, s.TargetKbps, s.RTTMs, s.FECLossPct, s.FECParityPct,
			float64(s.Report.LossPermille)/10, float64(s.Report.DecodeMicros)/1000, s.Video.Power)
	}
	return fmt.Sprintf("%s %s | %dx%d %.0f fps | encode p50 %.2f p99 %.2f ms | %.0f/%d kbps | rtt %.2f ms | fec loss %.1f%% parity %.0f%% | client loss %.1f%% decode %.2f ms | gpu %s",
		s.Client, s.Path, s.Video.Width, s.Video.Height, s.FPS, s.EncodeMs.P50, s.EncodeMs.P99, s.SentKbps, s.TargetKbps,
		s.RTTMs, s.FECLossPct, s.FECParityPct, float64(s.Report.LossPermille)/10, float64(s.Report.DecodeMicros)/1000, s.Video.Power)
}

// hostTelemetry is this run's usage telemetry and the setting it follows:
// on unless -no-telemetry or the app's Settings turned it off, read again
// before each send. Without a config directory it sends nothing.
func hostTelemetry(dir string, off bool) (*telemetry.Client, func() bool) {
	if dir == "" {
		d, err := config.Dir()
		if err != nil {
			return nil, func() bool { return false }
		}
		dir = d
	}
	on := func() bool { return !off && config.TelemetryOn(dir) }
	o := telemetry.OptionsForThisBuild(dir, version.Version, version.IsOfficialBuild(), telemetry.ModeHost)
	o.Enabled = on
	return telemetry.New(o, on()), on
}

// sessionTelemetry counts each client's stream. on reads the setting again
// first, so turning it off in the app reaches a host that runs already.
func sessionTelemetry(tel *telemetry.Client, on func() bool) func(session.SessionEvent) {
	return func(e session.SessionEvent) {
		tel.SetEnabled(on())
		s := telemetry.Session{Mode: telemetry.ModeHost, Codec: e.Codec.String(), Transport: e.Path.String()}
		if e.Started {
			tel.SessionStarted(s)
		} else {
			tel.SessionEnded(s)
		}
	}
}
