package transport

import (
	"encoding/binary"
	"fmt"
	"net"
	"net/netip"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/planckdotgg/planck/pkg/wire"
)

// TestLiveBurst sends synthetic video frames from one machine to another
// through the session's pacer and send train, and reports what arrived:
// lost runs, per-frame arrival spread, and the sender's time in send
// calls. Start the receiver first:
//
//	PLANCK_LIVE_BURST=recv PLANCK_LIVE_BURST_ADDR=:41500 go test -run TestLiveBurst -v ./pkg/transport
//	PLANCK_LIVE_BURST=send PLANCK_LIVE_BURST_ADDR=192.168.188.165:41500 \
//	  PLANCK_LIVE_BURST_CASES="500k@2000/16,500k@1000/16/nogso" go test -run TestLiveBurst -v ./pkg/transport
//
// A case is size[@mbps[/burst[/nogso]]]: frames of size bytes (k and m
// suffixes) at the pacing rate in Mbit/s (0 unpaced, "mac" a 2.5 Gbit/s
// LAN to a macOS client's receive queue) and burst datagrams;
// size may be "130k+500k/20" for a 500 KB frame every 20th.
// PLANCK_LIVE_BURST_SECONDS is each case's length (default 5) and
// PLANCK_LIVE_FPS the frame rate (default 120). PLANCK_LIVE_BURST_TRICKLE_US
// sends a 64-byte datagram that often between frames.
func TestLiveBurst(t *testing.T) {
	role, addr := os.Getenv("PLANCK_LIVE_BURST"), os.Getenv("PLANCK_LIVE_BURST_ADDR")
	switch {
	case role == "" || addr == "":
		t.Skip("set PLANCK_LIVE_BURST=send|recv and PLANCK_LIVE_BURST_ADDR")
	case role == "recv":
		burstReceive(t, addr)
	case role == "send":
		burstSend(t, addr)
	default:
		t.Fatalf("PLANCK_LIVE_BURST=%q", role)
	}
}

// burstHeader: case, frame, datagram index, datagram count, big flag.
const burstHeader = 1 + 4 + 2 + 2 + 1

// trickleCase marks the sender's between-frame datagrams.
const trickleCase = 255

type burstCase struct {
	name             string
	size, big, every int
	mbps, burst      int
	gso, mac         bool
}

func parseBurstCases(t *testing.T, s string) []burstCase {
	var out []burstCase
	for _, c := range strings.Split(s, ",") {
		bc := burstCase{name: c, burst: 16, gso: true}
		parts := strings.Split(c, "@")
		size := parts[0]
		if i := strings.IndexByte(size, '+'); i >= 0 {
			bigEvery := strings.SplitN(size[i+1:], "/", 2)
			bc.big = parseBytes(t, bigEvery[0])
			bc.every, _ = strconv.Atoi(bigEvery[1])
			size = size[:i]
		}
		bc.size = parseBytes(t, size)
		if len(parts) > 1 {
			f := strings.Split(parts[1], "/")
			bc.mbps, _ = strconv.Atoi(f[0])
			bc.mac = f[0] == "mac"
			if len(f) > 1 {
				bc.burst, _ = strconv.Atoi(f[1])
			}
			if len(f) > 2 && f[2] == "nogso" {
				bc.gso = false
			}
		}
		out = append(out, bc)
	}
	return out
}

func parseBytes(t *testing.T, s string) int {
	mult := 1
	switch {
	case strings.HasSuffix(s, "k"):
		mult, s = 1000, s[:len(s)-1]
	case strings.HasSuffix(s, "m"):
		mult, s = 1000_000, s[:len(s)-1]
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatalf("size %q: %v", s, err)
	}
	return n * mult
}

func envInt(name string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil && v > 0 {
		return v
	}
	return def
}

func burstSend(t *testing.T, addr string) {
	peer, err := netip.ParseAddrPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := listenUDP(nil, peer.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = sizeSocketBuffers(conn)
	cases := parseBurstCases(t, os.Getenv("PLANCK_LIVE_BURST_CASES"))
	fps := envInt("PLANCK_LIVE_FPS", 120)
	secs := envInt("PLANCK_LIVE_BURST_SECONDS", 5)
	const dgram = wire.MaxLANDatagram
	pkt := make([]byte, dgram)
	interval := int64(time.Second) / int64(fps)
	trickle := int64(envInt("PLANCK_LIVE_BURST_TRICKLE_US", 0)) * 1000
	tick := make([]byte, 64)
	tick[0] = trickleCase
	for ci, c := range cases {
		w := newTrainWriter(conn)
		w.gso = w.gso && c.gso
		var p Pacer
		var callNs, spreadNs []int64
		frames := secs * fps
		start := Mono() + 50*int64(time.Millisecond)
		for f := 0; f < frames; f++ {
			due := start + int64(f)*interval
			for trickle > 0 && Mono()+trickle < due {
				SleepUntilMono(Mono() + trickle)
				_, _ = conn.WriteToUDPAddrPort(tick, peer)
			}
			SleepUntilMono(due)
			size, big := c.size, byte(0)
			if c.every > 0 && f%c.every == c.every-1 {
				size, big = c.big, 1
			}
			count := (size + dgram - burstHeader - 1) / (dgram - burstHeader)
			rate := float64(c.mbps) * 1e6 / 8
			if c.mac {
				pc := LANPacing(2500)
				pc.Queue = MacReceiveQueue
				rate = pc.rate(size, big == 1, fps, 0)
			}
			p.SetRate(rate, c.burst*dgram)
			var inCalls int64
			first := Mono()
			for i := 0; i < count; i++ {
				if until := p.Reserve(dgram); until != 0 {
					t0 := Mono()
					_, _ = w.flush(peer)
					inCalls += Mono() - t0
					SleepUntilMono(until)
				}
				pkt[0] = byte(ci)
				binary.BigEndian.PutUint32(pkt[1:], uint32(f))
				binary.BigEndian.PutUint16(pkt[5:], uint16(i))
				binary.BigEndian.PutUint16(pkt[7:], uint16(count))
				pkt[9] = big
				t0 := Mono()
				if _, err := w.add(pkt, peer); err != nil {
					t.Fatal(err)
				}
				inCalls += Mono() - t0
			}
			t0 := Mono()
			_, _ = w.flush(peer)
			end := Mono()
			inCalls += end - t0
			callNs = append(callNs, inCalls)
			spreadNs = append(spreadNs, end-first)
		}
		t.Logf("case %d %-28s gso %-5v send calls p50 %s p99 %s, first-to-last sent p50 %s p99 %s",
			ci, c.name, w.gso, pctl(callNs, 50), pctl(callNs, 99), pctl(spreadNs, 50), pctl(spreadNs, 99))
		time.Sleep(500 * time.Millisecond)
	}
}

type burstFrame struct {
	count, got int
	big        bool
	seen       []bool
	first, end int64
	// maxGap is the longest silence before one of the frame's datagrams
	// after its first; sinceLast is from the first datagram of the frame
	// before it to this frame's first.
	maxGap, sinceLast int64
}

func burstReceive(t *testing.T, addr string) {
	la, err := net.ResolveUDPAddr("udp4", addr)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.ListenUDP("udp4", la)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	bufs, _ := sizeSocketBuffers(conn)
	t.Logf("listening on %s, buffers %+v", conn.LocalAddr(), bufs)
	wait := time.Duration(envInt("PLANCK_LIVE_BURST_WAIT", 120)) * time.Second
	frames := map[[2]uint32]*burstFrame{}
	read := newMediaReader(conn)
	buf := make([]byte, 2048)
	deadline := time.Now().Add(wait)
	var last time.Time
	var prev, prevFirst int64
	var gaps []int64
	for time.Now().Before(deadline) {
		n, _, err := read(buf)
		if err == nil && buf[0] == trickleCase {
			continue
		}
		if err != nil || n < burstHeader {
			if !last.IsZero() && time.Since(last) > 3*time.Second {
				break
			}
			continue
		}
		now := Mono()
		last = time.Now()
		gap := now - prev
		prev = now
		key := [2]uint32{uint32(buf[0]), binary.BigEndian.Uint32(buf[1:])}
		f := frames[key]
		if f == nil {
			count := int(binary.BigEndian.Uint16(buf[7:]))
			f = &burstFrame{count: count, big: buf[9] == 1, seen: make([]bool, count), first: now}
			if prevFirst != 0 {
				f.sinceLast = now - prevFirst
			}
			prevFirst = now
			frames[key] = f
		} else {
			f.maxGap = max(f.maxGap, gap)
			if gap > 2*int64(time.Millisecond) {
				gaps = append(gaps, gap)
			}
		}
		if i := int(binary.BigEndian.Uint16(buf[5:])); i < f.count && !f.seen[i] {
			f.seen[i] = true
			f.got++
			f.end = now
		}
	}
	type summary struct {
		frames, lossy, lost int
		runs                []int
		spread              []int64
	}
	byCase := map[uint32]map[bool]*summary{}
	for key, f := range frames {
		m := byCase[key[0]]
		if m == nil {
			m = map[bool]*summary{}
			byCase[key[0]] = m
		}
		s := m[f.big]
		if s == nil {
			s = &summary{}
			m[f.big] = s
		}
		s.frames++
		if f.got == f.count {
			s.spread = append(s.spread, f.end-f.first)
			continue
		}
		s.lossy++
		s.lost += f.count - f.got
		t.Logf("lossy: case %d frame %d got %d of %d, %.2fms after the frame before, longest silence inside %.2fms, missing %s",
			key[0], key[1], f.got, f.count, float64(f.sinceLast)/1e6, float64(f.maxGap)/1e6, missingRanges(f.seen))
		run := 0
		for _, ok := range append(f.seen, true) {
			if !ok {
				run++
			} else if run > 0 {
				s.runs = append(s.runs, run)
				run = 0
			}
		}
	}
	t.Logf("silences over 2 ms inside a frame: %d, p50 %s p99 %s max %s", len(gaps), pctl(gaps, 50), pctl(gaps, 99), pctl(gaps, 100))
	for _, c := range slices.Sorted(func(yield func(uint32) bool) {
		for k := range byCase {
			if !yield(k) {
				return
			}
		}
	}) {
		for _, big := range []bool{false, true} {
			s := byCase[c][big]
			if s == nil {
				continue
			}
			slices.Sort(s.runs)
			t.Logf("case %d big %-5v frames %5d lossy %3d lost %5d runs %v arrival spread p50 %s p99 %s",
				c, big, s.frames, s.lossy, s.lost, s.runs, pctl(s.spread, 50), pctl(s.spread, 99))
		}
	}
}

func missingRanges(seen []bool) string {
	var b strings.Builder
	for i := 0; i < len(seen); i++ {
		if seen[i] {
			continue
		}
		j := i
		for j+1 < len(seen) && !seen[j+1] {
			j++
		}
		fmt.Fprintf(&b, "%d-%d ", i, j)
		i = j
	}
	return b.String()
}

func pctl(v []int64, p int) string {
	if len(v) == 0 {
		return "-"
	}
	s := slices.Clone(v)
	slices.Sort(s)
	return fmt.Sprintf("%.3fms", float64(s[(len(s)-1)*p/100])/1e6)
}
