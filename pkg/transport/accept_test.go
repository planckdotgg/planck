package transport

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/planckdotgg/planck/pkg/wire"
	"github.com/quic-go/quic-go"
)

// stallPeer completes QUIC and TLS with a client certificate, as a real
// client does, and then never says hello.
func stallPeer(t *testing.T, sv *Server, cfg ClientConfig) *quic.Conn {
	t.Helper()
	tlsConf, err := ClientTLSConfig(cfg.Certificate, cfg.VerifyHost)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := quic.DialAddr(ctx, sv.Addr().String(), tlsConf, controlQUICConfig())
	if err != nil {
		t.Fatalf("stalled peer dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.CloseWithError(0, "") })
	return conn
}

func acceptForever(sv *Server) <-chan *Session {
	out := make(chan *Session, 4)
	go func() {
		for {
			s, err := sv.Accept(context.Background())
			var he *HandshakeError
			if errors.As(err, &he) {
				continue
			}
			if err != nil {
				return
			}
			out <- s
		}
	}()
	return out
}

func TestStalledPeerDoesNotBlockHandshakes(t *testing.T) {
	hostCert, clientCert := identities(t)
	cfg := pinServerConfig(hostCert)
	cfg.HandshakeTimeout = 20 * time.Second
	sv := startServer(t, cfg)
	ccfg := ClientConfig{
		Certificate: clientCert, Name: "mac", VerifyHost: PinnedHost(CertFingerprint(hostCert)),
		Auth: wire.AuthPIN, PIN: testPIN, HandshakeTimeout: 3 * time.Second,
		Session: wire.SessionConfig{Codec: wire.CodecHEVC, Slices: 1},
	}
	stallPeer(t, sv, ccfg)
	stallPeer(t, sv, ccfg)
	accepted := acceptForever(sv)
	time.Sleep(100 * time.Millisecond)

	start := time.Now()
	s, err := Dial(context.Background(), sv.Addr().String(), ccfg)
	if err != nil {
		t.Fatalf("client behind two stalled peers: %v after %v", err, time.Since(start))
	}
	defer s.Close()
	select {
	case hs := <-accepted:
		hs.Close()
	case <-time.After(3 * time.Second):
		t.Fatal("the host never accepted the session")
	}
}

func TestHandshakeLimitRefusesExtraPeers(t *testing.T) {
	hostCert, clientCert := identities(t)
	cfg := pinServerConfig(hostCert)
	cfg.HandshakeTimeout = 20 * time.Second
	sv := startServer(t, cfg)
	var refused []error
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			_, err := sv.Accept(context.Background())
			var he *HandshakeError
			if !errors.As(err, &he) {
				return
			}
			if errors.Is(err, ErrHandshakeBusy) {
				refused = append(refused, err)
				return
			}
		}
	}()
	ccfg := ClientConfig{Certificate: clientCert, VerifyHost: PinnedHost(CertFingerprint(hostCert))}
	for range maxHandshakes + 1 {
		stallPeer(t, sv, ccfg)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("no peer was refused over the handshake limit")
	}
	if len(refused) != 1 {
		t.Fatalf("refused %v", refused)
	}
}

func TestAcceptReportsDeadListener(t *testing.T) {
	hostCert, _ := identities(t)
	sv := startServer(t, pinServerConfig(hostCert))
	errc := make(chan error, 1)
	go func() {
		_, err := sv.Accept(context.Background())
		errc <- err
	}()
	_ = sv.ln.Close()
	select {
	case err := <-errc:
		var he *HandshakeError
		if err == nil || errors.As(err, &he) || errors.Is(err, ErrServerClosed) {
			t.Fatalf("accept on a listener that died: %v, want a listener error", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("accept never returned after the listener died")
	}
	sv.Close()
	if _, err := sv.Accept(context.Background()); !errors.Is(err, ErrServerClosed) {
		t.Fatalf("accept after Close: %v, want ErrServerClosed", err)
	}
}
