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
	"strings"
	"time"

	"github.com/planckdotgg/planck/internal/session"
	"github.com/planckdotgg/planck/pkg/config"
	"github.com/planckdotgg/planck/pkg/encode"
)

// defaultControlPort is planck-host's QUIC port, used when an address
// leaves the port out.
const defaultControlPort = config.DefaultControlPort

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
	noAudio        bool
	silentAudio    bool
	inputProbe     bool
	json           bool
	pathMode       pathMode
	timeout        time.Duration
}

func main() {
	o, err := parseOptions(os.Args[1:], os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
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
	var codec, chroma, mode, probe string
	fs.StringVar(&o.configDir, "config", "", "config directory (default: the user config dir /planck)")
	fs.StringVar(&o.name, "name", "", "device name hosts pair this client under (default: the machine name)")
	fs.BoolVar(&o.headless, "headless", false, "connect without a window, decode for -duration and print a summary")
	fs.StringVar(&o.connect, "connect", "", "headless: host address, host[:port]")
	fs.StringVar(&o.discover, "discover", "", "headless: find the host by name or host ID on the LAN and tailnet, and race its paths")
	fs.StringVar(&probe, "discovery-addrs", "", "headless: comma-separated discovery addresses (host:port) to probe instead of listening on the LAN and tailnet")
	fs.StringVar(&o.pin, "pin", "", "the host's 6-digit pairing PIN, needed the first time unless both devices are one Tailscale user's")
	fs.DurationVar(&o.duration, "duration", 10*time.Second, "headless: how long to stream")
	fs.DurationVar(&o.timeout, "timeout", 15*time.Second, "headless: how long discovery and the handshake may take")
	fs.StringVar(&codec, "codec", "", "headless: video codec to ask for: hevc, av1 or h264 (default: the host's choice)")
	fs.StringVar(&chroma, "chroma", "", "headless: chroma to ask for: nv12, p010, yuv444 or yuv444p10 (default: the host's choice)")
	fs.IntVar(&o.fps, "fps", 0, "headless: frame rate to ask for (default: the host's)")
	fs.IntVar(&o.bitrateKbps, "bitrate", 0, "headless: maximum video bitrate in kbps (default: the path's ceiling)")
	fs.IntVar(&o.slices, "slices", 0, "headless: FEC slices per frame to ask for (default: the host's)")
	fs.BoolVar(&o.noAudio, "no-audio", false, "headless: do not ask for audio")
	fs.BoolVar(&o.silentAudio, "silent-audio", false, "headless: receive and decode audio but play none of it")
	fs.BoolVar(&o.inputProbe, "input-probe", false, "headless: send a zero mouse move once a second, which moves nothing")
	fs.BoolVar(&o.json, "json", false, "headless: print the summary as JSON")
	fs.StringVar(&mode, "path-mode", string(pathAuto), "headless: auto, lan or tailscale; lan and tailscale race only those paths and fail on another")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if fs.NArg() > 0 {
		return o, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	var err error
	if codec != "" {
		if o.codec, err = session.ParseCodec(codec); err != nil {
			return o, err
		}
	}
	if chroma != "" {
		if o.chroma, err = session.ParseChroma(chroma); err != nil {
			return o, err
		}
	}
	switch m := pathMode(mode); m {
	case pathAuto, pathLAN, pathTailscale:
		o.pathMode = m
	default:
		return o, fmt.Errorf("-path-mode %q: want auto, lan or tailscale", mode)
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
	if o.name == "" {
		o.name, _ = os.Hostname()
	}
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
func withDefaultPort(addr string) string {
	if _, _, err := net.SplitHostPort(addr); err == nil {
		return addr
	}
	return net.JoinHostPort(strings.Trim(addr, "[]"), fmt.Sprint(defaultControlPort))
}
