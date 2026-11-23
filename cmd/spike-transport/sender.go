package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net"
	"net/netip"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/watzon/planck/pkg/transport"
)

const pingCount = 200

// Result is one benchmark run as seen from the host.
type Result struct {
	Cfg RunConfig `json:"cfg"`
	Rx  RxSummary `json:"rx"`

	MinRTTms float64 `json:"min_rtt_ms"`
	MedRTTms float64 `json:"med_rtt_ms"`

	Frames      int     `json:"frames"`
	Delivered   int     `json:"delivered"`
	DeliveryPct float64 `json:"delivery_pct"`

	LatP50   float64 `json:"lat_p50_ms"`
	LatP95   float64 `json:"lat_p95_ms"`
	LatP99   float64 `json:"lat_p99_ms"`
	LatMax   float64 `json:"lat_max_ms"`
	Net50    float64 `json:"net_p50_ms"`
	Net95    float64 `json:"net_p95_ms"`
	Net99    float64 `json:"net_p99_ms"`
	Ack50    float64 `json:"ack_rtt_p50_ms"`
	Ack99    float64 `json:"ack_rtt_p99_ms"`
	First50  float64 `json:"first_slice_p50_ms"`
	First99  float64 `json:"first_slice_p99_ms"`
	JitterMs float64 `json:"jitter_ms"`

	SendP99ms float64 `json:"send_p99_ms"`
	SlipP99ms float64 `json:"slip_p99_ms"`

	OfferedMbps   float64 `json:"offered_mbps"`
	RxMbps        float64 `json:"rx_mbps"`
	ParityPct     float64 `json:"parity_pct"`
	InjectedLoss  float64 `json:"injected_loss_pct"`
	ObservedLoss  float64 `json:"observed_loss_pct"`
	RecoveryPct   float64 `json:"fec_recovery_pct"`
	TxAllocsFrame float64 `json:"tx_allocs_per_frame"`
	RxAllocsFrame float64 `json:"rx_allocs_per_frame"`
	TxAllocsPkt   float64 `json:"tx_allocs_per_pkt"`
	RxAllocsPkt   float64 `json:"rx_allocs_per_pkt"`
	Error         string  `json:"error,omitempty"`
}

// txState is shared between the frame loop and the feedback reader.
type txState struct {
	ackAt    []atomic.Int64
	lead     []atomic.Uint32
	rtt      [pingCount]atomic.Int64
	adaptive *transport.AdaptiveParity
	lossEWMA float64
	lastFin  uint32
}

func (s *txState) handle(plain []byte) {
	now := transport.Mono()
	if len(plain) == 0 {
		return
	}
	switch plain[0] {
	case transport.KindPong:
		seq, ts, ok := transport.ParsePing(plain)
		if ok && int(seq) < pingCount {
			s.rtt[seq].Store(now - ts)
		}
	case transport.KindAck:
		var a transport.Ack
		if a.Parse(plain) != nil {
			return
		}
		if int(a.FrameID) < len(s.ackAt) {
			s.ackAt[a.FrameID].CompareAndSwap(0, now)
			s.lead[a.FrameID].Store(a.FirstSliceLeadUs)
		}
		if a.HasFinal && a.FinalFrameID > s.lastFin && a.FinalSentShards > 0 {
			s.lastFin = a.FinalFrameID
			l := 1 - float64(a.FinalRecvShards)/float64(a.FinalSentShards)
			s.lossEWMA += 0.05 * (max(l, 0) - s.lossEWMA)
			if s.adaptive != nil {
				s.adaptive.SetLoss(s.lossEWMA)
			}
		}
	}
}

func runSender(server string, mediaPort int, cfg RunConfig) (res Result) {
	res.Cfg = cfg
	if err := runSenderInner(server, mediaPort, cfg, &res); err != nil {
		res.Error = err.Error()
	}
	return res
}

func runSenderInner(server string, mediaPort int, cfg RunConfig, res *Result) error {
	fecPct, err := cfg.fecPercent()
	if err != nil {
		return err
	}
	frameBytes := int(cfg.BitrateMbps * 1e6 / 8 / float64(cfg.FPS))
	interval := time.Second / time.Duration(cfg.FPS)
	nFrames := cfg.DurationMs * cfg.FPS / 1000
	warmFrames := cfg.WarmupMs * cfg.FPS / 1000

	qc := transport.QUICConfig()
	qc.InitialPacketSize = uint16(cfg.QUICPacketSize)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := quic.DialAddr(ctx, server, transport.InsecureClientTLS(), qc)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.CloseWithError(0, "")
	ctl, err := conn.OpenStreamSync(ctx)
	if err != nil {
		return err
	}
	line, _ := json.Marshal(cfg)
	if _, err := ctl.Write(append(line, '\n')); err != nil {
		return err
	}
	rd := bufio.NewReader(ctl)
	if l, err := rd.ReadString('\n'); err != nil || l != "ready\n" {
		return fmt.Errorf("receiver not ready: %q %v", l, err)
	}

	st := &txState{
		ackAt: make([]atomic.Int64, nFrames),
		lead:  make([]atomic.Uint32, nFrames),
	}
	var policy transport.ParityPolicy = transport.FixedParity(fecPct)
	if fecPct < 0 {
		st.adaptive = &transport.AdaptiveParity{Target: 1e-3, MinLoss: 0.001}
		policy = st.adaptive
	}

	var send func([]byte) error
	var stopRx func()
	var wg sync.WaitGroup
	switch cfg.Transport {
	case "raw":
		suite, err := transport.ParseSuite(cfg.Suite)
		if err != nil {
			return err
		}
		keys, err := transport.MediaKeysFromConn(conn, suite)
		if err != nil {
			return err
		}
		sealer, _ := transport.NewSealer(keys.ClientToServer)
		opener, _ := transport.NewOpener(keys.ServerToClient)
		host, _, _ := net.SplitHostPort(server)
		hostIP, err := netip.ParseAddr(host)
		if err != nil {
			return err
		}
		dst := netip.AddrPortFrom(hostIP, uint16(mediaPort))
		udp, err := net.ListenUDP("udp", nil)
		if err != nil {
			return err
		}
		defer udp.Close()
		udp.SetWriteBuffer(8 << 20)
		udp.SetReadBuffer(4 << 20)
		var sealMu sync.Mutex
		send = func(pkt []byte) error {
			sealMu.Lock()
			w := sealer.Seal(pkt, pkt[transport.AEADHeaderLen:])
			_, err := udp.WriteToUDPAddrPort(w, dst)
			sealMu.Unlock()
			return err
		}
		var foreign atomic.Int64
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf := make([]byte, 2048)
			for {
				n, from, err := udp.ReadFromUDPAddrPort(buf)
				if err != nil {
					var ne net.Error
					if errors.As(err, &ne) && ne.Timeout() {
						return
					}
					if errors.Is(err, net.ErrClosed) {
						return
					}
					continue
				}
				if plain, err := opener.Open(buf[:n]); err == nil {
					st.handle(plain)
				} else if foreign.Add(1) == 1 {
					log.Printf("raw: dropped packet from %v: %v", from, err)
				}
			}
		}()
		stopRx = func() { udp.SetReadDeadline(time.Now()) }
	case "quic":
		send = func(pkt []byte) error { return conn.SendDatagram(pkt[transport.AEADHeaderLen:]) }
		qctx, qcancel := context.WithCancel(context.Background())
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				b, err := conn.ReceiveDatagram(qctx)
				if err != nil {
					return
				}
				st.handle(b)
			}
		}()
		stopRx = qcancel
	default:
		return fmt.Errorf("unknown transport %q", cfg.Transport)
	}

	pingBuf := make([]byte, transport.PacketBufLen(64))
	for i := 0; i < pingCount; i++ {
		p := transport.PutPing(pingBuf[transport.AEADHeaderLen:], transport.KindPing, uint32(i), transport.Mono())
		if err := send(pingBuf[:transport.AEADHeaderLen+len(p)]); err != nil {
			return fmt.Errorf("ping: %w", err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	var rtts []float64
	for i := range st.rtt {
		if v := st.rtt[i].Load(); v > 0 {
			rtts = append(rtts, float64(v)/1e6)
		}
	}
	if len(rtts) < pingCount/2 {
		return fmt.Errorf("only %d/%d pongs", len(rtts), pingCount)
	}
	sort.Float64s(rtts)
	res.MinRTTms = rtts[0]
	res.MedRTTms = rtts[len(rtts)/2]

	frame := make([]byte, frameBytes)
	rand.Read(frame)
	pk := transport.NewPacketizer(cfg.ShardSize)
	pacer := transport.NewPacer(cfg.PaceMbps * 1e6 / 8)
	im := transport.NewImpairer(transport.Impairment{
		Loss:   cfg.Loss,
		Jitter: time.Duration(cfg.JitterMs * float64(time.Millisecond)),
		Seed:   uint64(time.Now().UnixNano()),
	}, transport.PacketBufLen(cfg.ShardSize), send)
	var dataShards, parityShards, wireBytes uint64
	var sendErr error
	emit := func(pkt []byte) error {
		if pkt[transport.AEADHeaderLen+3] < pkt[transport.AEADHeaderLen+4] {
			dataShards++
		} else {
			parityShards++
		}
		wireBytes += uint64(len(pkt) - transport.AEADHeaderLen)
		pacer.Wait(len(pkt) + transport.AEADTagLen)
		return im.Send(pkt)
	}

	started := make([]int64, nFrames)
	sendDur := make([]float64, 0, nFrames)
	slip := make([]float64, 0, nFrames)
	originNs := transport.Mono() + int64(5*time.Millisecond)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for i := 0; i < nFrames; i++ {
		due := originNs + int64(i)*interval.Nanoseconds()
		transport.SleepUntilMono(due)
		start := transport.Mono()
		started[i] = start
		if err := pk.Packetize(uint32(i), frame, cfg.Slices, policy, emit); err != nil {
			sendErr = err
			break
		}
		if i >= warmFrames {
			sendDur = append(sendDur, float64(transport.Mono()-start)/1e6)
			slip = append(slip, float64(start-due)/1e6)
		}
	}
	runtime.ReadMemStats(&after)
	elapsed := float64(transport.Mono()-originNs) / 1e9
	if err := im.Close(); err != nil && sendErr == nil {
		sendErr = err
	}
	time.Sleep(300 * time.Millisecond)
	stopRx()
	wg.Wait()

	if _, err := ctl.Write([]byte("stop\n")); err != nil {
		return err
	}
	sumLine, err := rd.ReadBytes('\n')
	if err != nil {
		return fmt.Errorf("summary: %w", err)
	}
	if err := json.Unmarshal(sumLine, &res.Rx); err != nil {
		return err
	}
	if sendErr != nil {
		return fmt.Errorf("send: %w", sendErr)
	}

	half := res.MinRTTms / 2
	var lat, first, netLat, ackRTT []float64
	for i := warmFrames; i < nFrames; i++ {
		res.Frames++
		a := st.ackAt[i].Load()
		if a == 0 {
			continue
		}
		res.Delivered++
		due := float64(originNs+int64(i)*interval.Nanoseconds()) / 1e6
		l := float64(a)/1e6 - due - half
		lat = append(lat, l)
		ackRTT = append(ackRTT, float64(a-started[i])/1e6)
		netLat = append(netLat, float64(a-started[i])/1e6-half)
		first = append(first, l-float64(st.lead[i].Load())/1e3)
	}
	res.DeliveryPct = 100 * float64(res.Delivered) / float64(max(res.Frames, 1))
	if len(lat) > 0 {
		res.JitterMs = rfc3550Jitter(lat)
		sort.Float64s(lat)
		sort.Float64s(first)
		sort.Float64s(netLat)
		sort.Float64s(ackRTT)
		res.Net50, res.Net95, res.Net99 = percentile(netLat, 50), percentile(netLat, 95), percentile(netLat, 99)
		res.Ack50, res.Ack99 = percentile(ackRTT, 50), percentile(ackRTT, 99)
		res.LatP50, res.LatP95, res.LatP99, res.LatMax = percentile(lat, 50), percentile(lat, 95), percentile(lat, 99), lat[len(lat)-1]
		res.First50, res.First99 = percentile(first, 50), percentile(first, 99)
	}
	sort.Float64s(sendDur)
	sort.Float64s(slip)
	res.SendP99ms = percentile(sendDur, 99)
	res.SlipP99ms = percentile(slip, 99)
	res.OfferedMbps = float64(wireBytes) * 8 / elapsed / 1e6
	if res.Rx.ActiveSec > 0 {
		res.RxMbps = float64(res.Rx.MediaBytes) * 8 / res.Rx.ActiveSec / 1e6
	}
	if dataShards > 0 {
		res.ParityPct = 100 * float64(parityShards) / float64(dataShards)
	}
	sent := im.Sent + im.Dropped
	if sent > 0 {
		res.InjectedLoss = 100 * float64(im.Dropped) / float64(sent)
		res.ObservedLoss = 100 * (1 - float64(res.Rx.Packets)/float64(sent))
	}
	if n := res.Rx.SlicesRecovered + res.Rx.SlicesLost; n > 0 {
		res.RecoveryPct = 100 * float64(res.Rx.SlicesRecovered) / float64(n)
	}
	res.TxAllocsFrame = float64(after.Mallocs-before.Mallocs) / float64(nFrames)
	res.TxAllocsPkt = float64(after.Mallocs-before.Mallocs) / float64(max(sent, 1))
	if res.Rx.FramesComplete+res.Rx.FramesIncomplete > 0 {
		res.RxAllocsFrame = float64(res.Rx.Mallocs) / float64(res.Rx.FramesComplete+res.Rx.FramesIncomplete)
	}
	res.RxAllocsPkt = float64(res.Rx.Mallocs) / float64(max(res.Rx.Packets, 1))
	return nil
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return math.NaN()
	}
	idx := int(math.Ceil(p/100*float64(len(sorted)))) - 1
	return sorted[min(max(idx, 0), len(sorted)-1)]
}

// rfc3550Jitter is the RFC 3550 interarrival jitter over per-frame transit
// times, in the same unit as the input.
func rfc3550Jitter(transit []float64) float64 {
	var j float64
	for i := 1; i < len(transit); i++ {
		j += (math.Abs(transit[i]-transit[i-1]) - j) / 16
	}
	return j
}
