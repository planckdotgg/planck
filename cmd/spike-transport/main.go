// Command spike-transport benchmarks Planck media transports between a host
// (sender) and a client (receiver): quic-go RFC 9221 datagrams versus raw UDP
// sealed with keys exported from a QUIC control connection, with sub-frame
// slicing, Reed-Solomon FEC and simulated loss and jitter.
//
//	client: spike-transport recv -listen :47998 -media-port 47999
//	host:   spike-transport send -server 192.168.188.138:47998 -matrix cc
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "recv":
		fs := flag.NewFlagSet("recv", flag.ExitOnError)
		listen := fs.String("listen", ":47998", "QUIC control/datagram listen address")
		mediaPort := fs.Int("media-port", 47999, "raw AEAD UDP media port")
		fs.Parse(os.Args[2:])
		log.Fatal(runReceiver(*listen, *mediaPort))
	case "send":
		fs := flag.NewFlagSet("send", flag.ExitOnError)
		server := fs.String("server", "", "receiver control address host:port")
		tsServer := fs.String("ts-server", "", "receiver control address over Tailscale, for the ts matrix")
		mediaPort := fs.Int("media-port", 47999, "receiver raw media port")
		matrix := fs.String("matrix", "smoke", "comma-separated: smoke, cc, fec, ts")
		dur := fs.Duration("duration", 4*time.Second, "measured duration per run, after warm-up")
		warm := fs.Duration("warmup", 500*time.Millisecond, "warm-up excluded from stats")
		suite := fs.String("suite", "aes128gcm", "raw AEAD suite: aes128gcm or chacha20poly1305")
		out := fs.String("out", "results.json", "JSON results file")
		fs.Parse(os.Args[2:])
		if *server == "" {
			usage()
		}
		runs := buildMatrix(*matrix, *suite, *dur+*warm, *warm, *tsServer != "")
		var results []Result
		fmt.Println(header())
		for _, cfg := range runs {
			target := *server
			if cfg.Path == "ts" {
				target = *tsServer
			}
			r := runSender(target, *mediaPort, cfg)
			fmt.Println(row(r))
			results = append(results, r)
			b, _ := json.MarshalIndent(results, "", " ")
			os.WriteFile(*out, b, 0o644)
			time.Sleep(300 * time.Millisecond)
		}
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: spike-transport recv [-listen :47998] | send -server host:port [-matrix smoke,cc,fec,ts]")
	os.Exit(2)
}

const (
	lanShard    = 1360
	lanQUICPkt  = 1452
	tsShard     = 1100
	tsQUICPkt   = 1200
	rawPaceMbps = 500
)

func buildMatrix(names, suite string, total, warm time.Duration, haveTS bool) []RunConfig {
	base := RunConfig{
		Path: "lan", Suite: suite, FPS: 240, Slices: 4, FEC: "0", BitrateMbps: 100,
		ShardSize: lanShard, QUICPacketSize: lanQUICPkt,
		DurationMs: int(total / time.Millisecond), WarmupMs: int(warm / time.Millisecond),
	}
	type tr struct {
		name string
		pace float64
	}
	transports := []tr{{"quic", 0}, {"raw", 0}, {"raw", rawPaceMbps}}
	var runs []RunConfig
	for _, name := range strings.Split(names, ",") {
		switch name {
		case "smoke":
			for _, t := range transports {
				c := base
				c.Transport, c.PaceMbps = t.name, t.pace
				c.DurationMs, c.WarmupMs = 1500, 300
				runs = append(runs, c)
			}
		case "cc":
			for _, mbps := range []float64{50, 100, 150} {
				for _, fps := range []int{120, 240} {
					for _, slices := range []int{1, 4} {
						for _, t := range transports {
							c := base
							c.Transport, c.PaceMbps = t.name, t.pace
							c.BitrateMbps, c.FPS, c.Slices = mbps, fps, slices
							runs = append(runs, c)
						}
					}
				}
			}
		case "fec":
			type imp struct{ loss, jitter float64 }
			for _, im := range []imp{{0, 0}, {0.01, 0}, {0.05, 0}, {0.01, 2}} {
				for _, slices := range []int{1, 4} {
					for _, fec := range []string{"0", "10", "20", "adaptive"} {
						for _, t := range []tr{{"quic", 0}, {"raw", rawPaceMbps}} {
							c := base
							c.Transport, c.PaceMbps = t.name, t.pace
							c.Slices, c.FEC, c.Loss, c.JitterMs = slices, fec, im.loss, im.jitter
							runs = append(runs, c)
						}
					}
				}
			}
		case "ts":
			paths := []string{"lan"}
			if haveTS {
				paths = append(paths, "ts")
			}
			for _, mbps := range []float64{50, 100, 150} {
				for _, path := range paths {
					for _, t := range transports {
						c := base
						c.Path = path
						c.Transport, c.PaceMbps, c.BitrateMbps = t.name, t.pace, mbps
						c.ShardSize, c.QUICPacketSize = tsShard, tsQUICPkt
						runs = append(runs, c)
					}
				}
			}
		default:
			log.Fatalf("unknown matrix %q", name)
		}
	}
	return runs
}

func header() string {
	return fmt.Sprintf("%-58s %5s %6s | %6s %6s %6s | %6s %6s %6s %6s | %6s %6s %6s %6s %6s | %7s %7s %5s %5s %5s | %6s %6s",
		"run", "rtt", "deliv%", "p50", "p95", "p99", "net50", "net95", "net99", "ack99", "1st50", "jit", "send99", "slip99", "max",
		"offerMb", "rxMb", "par%", "loss%", "rec%", "txA/f", "rxA/f")
}

func row(r Result) string {
	if r.Error != "" {
		return fmt.Sprintf("%-58s ERROR %s", r.Cfg.label(), r.Error)
	}
	return fmt.Sprintf("%-58s %5.2f %6.2f | %6.2f %6.2f %6.2f | %6.2f %6.2f %6.2f %6.2f | %6.2f %6.3f %6.2f %6.2f %6.1f | %7.1f %7.1f %5.1f %5.2f %5.1f | %6.1f %6.1f",
		r.Cfg.label(), r.MinRTTms, r.DeliveryPct, r.LatP50, r.LatP95, r.LatP99, r.Net50, r.Net95, r.Net99, r.Ack99,
		r.First50, r.JitterMs, r.SendP99ms, r.SlipP99ms, r.LatMax,
		r.OfferedMbps, r.RxMbps, r.ParityPct, r.ObservedLoss, r.RecoveryPct, r.TxAllocsFrame, r.RxAllocsFrame)
}
