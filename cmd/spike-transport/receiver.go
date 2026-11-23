package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/planckdotgg/planck/pkg/transport"
)

const firstSliceRing = 64

// rxSession reassembles one run's media and answers pings with pongs and
// completed frames with acks. It runs on a single goroutine.
type rxSession struct {
	ra       *transport.Reassembler
	reply    func(pkt []byte) error
	tx       []byte
	firstID  [firstSliceRing]uint32
	firstAt  [firstSliceRing]int64
	final    transport.FrameStats
	hasFinal bool
	sum      RxSummary
	start    int64
	last     int64
}

func newRxSession(shardSize int, reply func([]byte) error) *rxSession {
	s := &rxSession{reply: reply, tx: make([]byte, 256)}
	s.ra = transport.NewReassembler(shardSize, func(st transport.FrameStats) {
		if st.Complete {
			s.sum.FramesComplete++
		} else {
			s.sum.FramesIncomplete++
		}
		s.sum.SlicesRecovered += uint64(st.SlicesRecovered)
		s.sum.SlicesLost += uint64(st.SlicesLost)
		s.sum.RecvShards += uint64(st.RecvShards)
		s.sum.SentShards += uint64(st.SentShards)
		s.final = st
		s.hasFinal = true
	})
	return s
}

func (s *rxSession) handle(plain []byte) {
	if len(plain) == 0 {
		return
	}
	now := transport.Mono()
	switch plain[0] {
	case transport.KindPing:
		seq, ts, ok := transport.ParsePing(plain)
		if ok {
			pong := transport.PutPing(s.tx[transport.AEADHeaderLen:], transport.KindPong, seq, ts)
			s.reply(s.tx[:transport.AEADHeaderLen+len(pong)])
		}
	case transport.KindMedia:
		if s.sum.Packets == 0 {
			s.start = now
		}
		s.last = now
		s.sum.Packets++
		s.sum.MediaBytes += uint64(len(plain))
		res, err := s.ra.Push(plain)
		if err != nil || !res.SliceReady {
			return
		}
		slot := res.FrameID % firstSliceRing
		if s.firstID[slot] != res.FrameID+1 {
			s.firstID[slot] = res.FrameID + 1
			s.firstAt[slot] = now
		}
		if res.FrameComplete {
			ack := transport.Ack{
				FrameID:          res.FrameID,
				FirstSliceLeadUs: uint32((now - s.firstAt[slot]) / 1e3),
			}
			if s.hasFinal {
				ack.HasFinal = true
				ack.FinalFrameID = s.final.FrameID
				ack.FinalRecvShards = uint16(min(s.final.RecvShards, 65535))
				ack.FinalSentShards = uint16(s.final.SentShards)
				ack.FinalFrameOK = s.final.Complete
			}
			b := ack.Put(s.tx[transport.AEADHeaderLen:])
			s.reply(s.tx[:transport.AEADHeaderLen+len(b)])
		}
	}
}

func (s *rxSession) finish() RxSummary {
	s.ra.Flush()
	s.sum.Invalid = s.ra.Invalid
	s.sum.Late = s.ra.Late
	if s.sum.Packets > 1 {
		s.sum.ActiveSec = float64(s.last-s.start) / 1e9
	}
	return s.sum
}

type receiver struct {
	media *net.UDPConn
	mu    sync.Mutex
}

func runReceiver(listen string, mediaPort int) error {
	tlsConf, err := transport.SelfSignedServerTLS()
	if err != nil {
		return err
	}
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return err
	}
	media, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP(host), Port: mediaPort})
	if err != nil {
		return err
	}
	media.SetReadBuffer(8 << 20)
	media.SetWriteBuffer(4 << 20)

	ln, err := quic.ListenAddr(listen, tlsConf, receiverQUICConfig())
	if err != nil {
		return err
	}
	log.Printf("receiver: control %s, raw media udp :%d", ln.Addr(), mediaPort)
	r := &receiver{media: media}
	for {
		conn, err := ln.Accept(context.Background())
		if err != nil {
			return err
		}
		go func() {
			if err := r.serve(conn); err != nil {
				log.Printf("session %s: %v", conn.RemoteAddr(), err)
			}
		}()
	}
}

func receiverQUICConfig() *quic.Config {
	c := transport.QUICConfig()
	c.InitialPacketSize = 1200
	return c
}

func (r *receiver) serve(conn *quic.Conn) error {
	defer conn.CloseWithError(0, "")
	r.mu.Lock()
	defer r.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	st, err := conn.AcceptStream(ctx)
	if err != nil {
		return err
	}
	rd := bufio.NewReader(st)
	line, err := rd.ReadBytes('\n')
	if err != nil {
		return err
	}
	var cfg RunConfig
	if err := json.Unmarshal(line, &cfg); err != nil {
		return err
	}

	var sum RxSummary
	var before runtime.MemStats
	var wg sync.WaitGroup
	var sess *rxSession
	stop := func() {}

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
		opener, _ := transport.NewOpener(keys.ClientToServer)
		sealer, _ := transport.NewSealer(keys.ServerToClient)
		var peer netip.AddrPort
		sess = newRxSession(cfg.ShardSize, func(pkt []byte) error {
			w := sealer.Seal(pkt, pkt[transport.AEADHeaderLen:])
			_, err := r.media.WriteToUDPAddrPort(w, peer)
			return err
		})
		r.media.SetReadDeadline(time.Time{})
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf := make([]byte, 2048)
			for {
				n, from, err := r.media.ReadFromUDPAddrPort(buf)
				if err != nil {
					var ne net.Error
					if errors.As(err, &ne) && ne.Timeout() {
						return
					}
					continue
				}
				plain, err := opener.Open(buf[:n])
				if err != nil {
					sess.sum.AuthFail++
					continue
				}
				peer = from
				sess.handle(plain)
			}
		}()
		stop = func() { r.media.SetReadDeadline(time.Now()) }
	case "quic":
		qctx, qcancel := context.WithCancel(context.Background())
		sess = newRxSession(cfg.ShardSize, func(pkt []byte) error {
			return conn.SendDatagram(pkt[transport.AEADHeaderLen:])
		})
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				b, err := conn.ReceiveDatagram(qctx)
				if err != nil {
					return
				}
				sess.handle(b)
			}
		}()
		stop = qcancel
	default:
		return fmt.Errorf("unknown transport %q", cfg.Transport)
	}
	runtime.ReadMemStats(&before)

	if _, err := st.Write([]byte("ready\n")); err != nil {
		stop()
		wg.Wait()
		return err
	}
	if _, err := rd.ReadBytes('\n'); err != nil {
		log.Printf("control read: %v", err)
	}
	stop()
	wg.Wait()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	sum = sess.finish()
	sum.Mallocs = after.Mallocs - before.Mallocs
	sum.NumGC = after.NumGC - before.NumGC
	out, _ := json.Marshal(sum)
	st.Write(append(out, '\n'))
	st.Close()
	fmt.Fprintf(os.Stderr, "run %-60s pkts=%d complete=%d incomplete=%d rec=%d lost=%d authfail=%d\n",
		cfg.label(), sum.Packets, sum.FramesComplete, sum.FramesIncomplete, sum.SlicesRecovered, sum.SlicesLost, sum.AuthFail)
	time.Sleep(50 * time.Millisecond)
	return nil
}
