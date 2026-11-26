// Command planck-client is the Planck desktop client: the native window
// with the hosts, pairing and settings screens and the stream view. With
// -headless it connects without a window, decodes on the GPU for a while
// and prints a latency and throughput summary, for benchmarks and
// end-to-end tests against a live planck-host.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/planckdotgg/planck/internal/session"
	"github.com/planckdotgg/planck/internal/version"
	"github.com/planckdotgg/planck/pkg/config"
	"github.com/planckdotgg/planck/pkg/defaults"
	"github.com/planckdotgg/planck/pkg/discovery"
	"github.com/planckdotgg/planck/pkg/encode"
)

// defaultControlPort is planck-host's QUIC port, used when an address
// leaves the port out.
const defaultControlPort = config.DefaultControlPort

var errShowVersion = errors.New("-version")

type pathMode string

const (
	pathAuto      pathMode = "auto"
	pathLAN       pathMode = "lan"
	pathTailscale pathMode = "tailscale"
)

type options struct {
	configDir string
	name      string

	headless bool
	connect  string
	discover string
	// discoveryAddrs, when set, are probed instead of listening for LAN
	// beacons and probing the tailnet.
	discoveryAddrs []netip.AddrPort
	pin            string
	duration       time.Duration
	codec          encode.Codec
	chroma         encode.Chroma
	fps            int
	bitrateKbps    int
	slices         int
	// overrides go to hosts that resolve the stream from both ends'
	// capabilities; the fields above go to older hosts.
	overrides   defaults.UserOverrides
	noAudio     bool
	silentAudio bool
	inputProbe  bool
	micTone     bool
	inputEvery  time.Duration
	json        bool
	pathMode    pathMode
	timeout     time.Duration
	// discoveryPort is where hosts answer discovery, on the LAN and the
	// tailnet.
	discoveryPort uint16
}

func main() {
	o, err := parseOptions(os.Args[1:], os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if errors.Is(err, errShowVersion) {
		fmt.Println("planck-client", version.Full())
		return
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "planck-client:", err)
		os.Exit(2)
	}
	if o.headless {
		err = runHeadless(o, os.Stdout)
	} else {
		err = runGUI(o)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "planck-client:", err)
		os.Exit(1)
	}
}

func parseOptions(args []string, out io.Writer) (options, error) {
	var o options
	fs := flag.NewFlagSet("planck-client", flag.ContinueOnError)
	fs.SetOutput(out)
	var codec, chroma, mode, probe, fps, slices string
	var k session.Knobs
	fs.StringVar(&o.configDir, "config", "", "config directory (default: the user config dir /planck)")
	fs.StringVar(&o.name, "name", "", "device name hosts pair this client under for this run (default: the one kept in Settings, or the machine name)")
	fs.BoolVar(&o.headless, "headless", false, "connect without a window, decode for -duration and print a summary")
	fs.StringVar(&o.connect, "connect", "", "headless: host address, host[:port]")
	fs.StringVar(&o.discover, "discover", "", "headless: find the host by name or host ID on the LAN and tailnet, and race its paths")
	discoveryPort := fs.Uint("discovery-port", discovery.DefaultPort, "the hosts' discovery port, listened on for LAN beacons and probed on the LAN and the tailnet")
	fs.StringVar(&probe, "discovery-addrs", "", "headless: comma-separated discovery addresses (host:port) to probe instead of listening on the LAN and tailnet")
	fs.StringVar(&o.pin, "pin", "", "the host's 6-digit pairing PIN, needed the first time unless both devices are one Tailscale user's")
	fs.DurationVar(&o.duration, "duration", 10*time.Second, "headless: how long to stream")
	fs.DurationVar(&o.timeout, "timeout", 15*time.Second, "headless: how long discovery and the handshake may take")
	fs.StringVar(&codec, "codec", "auto", "video codec: auto, pyrowave, hevc, av1 or h264")
	fs.StringVar(&chroma, "chroma", "auto", "chroma: auto, 420 or 444 (or nv12, p010, yuv444, yuv444p10)")
	fs.StringVar(&k.BitDepth, "bit-depth", "auto", "bit depth: auto, 8 or 10")
	fs.StringVar(&k.Resolution, "res", "auto", "stream size: auto (this screen's), 1080p, 1440p, 4k or WxH; needs the host's virtual display")
	fs.StringVar(&fps, "fps", "auto", "frame rate: auto, 60, 120, 144 or 240")
	fs.IntVar(&o.bitrateKbps, "bitrate", 0, "maximum video bitrate in kbps, for hosts that do not resolve the stream (default: the path's ceiling)")
	fs.IntVar(&k.BitrateMbps, "bitrate-mbps", 0, "video bitrate in Mbps (0: auto)")
	fs.StringVar(&slices, "slices", "auto", "codec slices sent as they finish: auto, 1, 4 or 8")
	fs.StringVar(&k.VDD, "vdd", "auto", "host virtual display at this screen's size: auto, on or off")
	fs.StringVar(&k.VDDGPU, "vdd-gpu", "", `host GPU that renders the virtual display and encodes, by name ("4080", "Radeon")`)
	fs.StringVar(&k.Split, "nvenc-split", "auto", "NVENC two-engine split-frame encode: auto, on or off")
	fs.StringVar(&k.Direct, "direct-display", "auto", "macOS full-screen direct-to-display: auto, on or off")
	fs.StringVar(&k.VSync, "vsync", "auto", "present one frame a refresh, in step with the display: auto (off on the LAN), on or off")
	fs.StringVar(&k.Display, "display", "", "the host display to stream, by its ID; empty lets the host choose")
	fs.StringVar(&k.HDR10, "hdr10", "auto", "HDR10 output: auto (where both ends and the host desktop have HDR) or off")
	fs.BoolVar(&o.noAudio, "no-audio", false, "headless: do not ask for audio")
	fs.BoolVar(&o.silentAudio, "silent-audio", false, "headless: receive and decode audio but play none of it")
	fs.BoolVar(&o.inputProbe, "input-probe", false, "headless: send a zero mouse move once a second, which moves nothing")
	fs.BoolVar(&o.micTone, "mic-tone", false, "headless: unmute the microphone and send a 440 Hz tone in place of it, into the host's virtual mic")
	fs.DurationVar(&o.inputEvery, "input-probe-every", time.Second, "headless: how often -input-probe sends")
	fs.BoolVar(&o.json, "json", false, "headless: print the summary as JSON")
	fs.StringVar(&mode, "path-mode", string(pathAuto), "auto, lan or tailscale; headless, lan and tailscale race only those paths and fail on another")
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
	var err error
	if codec != "" && codec != "auto" {
		if o.codec, err = session.ParseCodec(codec); err != nil {
			return o, err
		}
		k.Codec = codec
	}
	if chroma != "" && chroma != "auto" {
		if o.chroma, err = session.ParseChroma(chroma); err != nil {
			return o, err
		}
		k.Chroma = chroma
	}
	if fps != "auto" {
		if o.fps, err = strconv.Atoi(fps); err != nil {
			return o, fmt.Errorf("-fps %q: want auto, 60, 120, 144 or 240", fps)
		}
		k.FPS = fps
	}
	if slices != "auto" {
		if o.slices, err = strconv.Atoi(slices); err != nil {
			return o, fmt.Errorf("-slices %q: want auto, 1, 4 or 8", slices)
		}
		k.Slices = slices
	}
	if o.bitrateKbps == 0 {
		o.bitrateKbps = k.BitrateMbps * 1000
	}
	switch m := pathMode(mode); m {
	case pathAuto, pathLAN, pathTailscale:
		o.pathMode = m
		k.Route = mode
	default:
		return o, fmt.Errorf("-path-mode %q: want auto, lan or tailscale", mode)
	}
	if o.overrides, err = k.Overrides(); err != nil {
		return o, fmt.Errorf("overrides: %w", err)
	}
	for _, s := range strings.Split(probe, ",") {
		if s = strings.TrimSpace(s); s == "" {
			continue
		}
		ap, err := netip.ParseAddrPort(s)
		if err != nil {
			return o, fmt.Errorf("-discovery-addrs: %w", err)
		}
		o.discoveryAddrs = append(o.discoveryAddrs, ap)
	}
	if *discoveryPort == 0 || *discoveryPort > 65535 {
		return o, fmt.Errorf("-discovery-port %d out of range", *discoveryPort)
	}
	o.discoveryPort = uint16(*discoveryPort)
	switch {
	case o.pin != "" && (len(o.pin) != 6 || strings.Trim(o.pin, "0123456789") != ""):
		return o, fmt.Errorf("-pin %q: want 6 digits", o.pin)
	case o.fps < 0 || o.bitrateKbps < 0 || o.slices < 0 || o.slices > 16:
		return o, errors.New("-fps and -bitrate must not be negative, -slices 0 to 16")
	case o.noAudio && o.silentAudio:
		return o, errors.New("-no-audio and -silent-audio exclude each other")
	case !o.headless && (o.connect != "" || o.discover != ""):
		return o, errors.New("-connect and -discover need -headless")
	case o.headless && (o.connect == "") == (o.discover == ""):
		return o, errors.New("-headless needs one of -connect or -discover")
	case o.headless && o.duration <= 0:
		return o, errors.New("-duration must be positive")
	}
	return o, nil
}

// withDefaultPort adds planck-host's control port to an address without
// one.
// deviceName is the name hosts pair this client under: -name, else the
// one client.json keeps in dir, else the machine's.
func deviceName(dir, flagName string) string {
	if flagName != "" {
		return flagName
	}
	if dir == "" {
		if d, err := config.Dir(); err == nil {
			dir = d
		}
	}
	if c, err := config.LoadClientConfig(dir); err == nil && c.DeviceName != "" {
		return c.DeviceName
	}
	return config.Hostname()
}

func withDefaultPort(addr string) string {
	if _, _, err := net.SplitHostPort(addr); err == nil {
		return addr
	}
	return net.JoinHostPort(strings.Trim(addr, "[]"), fmt.Sprint(defaultControlPort))
}
