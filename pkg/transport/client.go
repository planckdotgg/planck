package transport

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/planckdotgg/planck/pkg/wire"
)

var ErrHostPINMismatch = errors.New("transport: host could not prove the pairing PIN")

type ClientConfig struct {
	Certificate tls.Certificate
	Name        string
	// VerifyHost decides whether to trust the host's certificate
	// fingerprint. Required.
	VerifyHost HostVerifier
	Auth       wire.AuthMethod
	// PIN is the pairing PIN the user read off the host, for AuthPIN.
	PIN              string
	Session          wire.SessionConfig
	Handlers         Handlers
	PingInterval     time.Duration
	HandshakeTimeout time.Duration
	// SimulateLoss drops this fraction of outgoing media datagrams, for
	// tests and diagnostics.
	SimulateLoss float64
}

// Dial connects to a host, authenticates, negotiates the session and brings
// up the media path.
func Dial(ctx context.Context, addr string, cfg ClientConfig) (*Session, error) {
	if len(cfg.Certificate.Certificate) == 0 {
		return nil, errors.New("transport: ClientConfig.Certificate is required")
	}
	tlsConf, err := ClientTLSConfig(cfg.Certificate, cfg.VerifyHost)
	if err != nil {
		return nil, err
	}
	if cfg.Auth == 0 {
		cfg.Auth = wire.AuthPaired
	}
	timeout := cfg.HandshakeTimeout
	if timeout <= 0 {
		timeout = defaultHandshakeTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := quic.DialAddr(ctx, addr, tlsConf, controlQUICConfig())
	if err != nil {
		return nil, err
	}
	s, err := clientHandshake(ctx, conn, cfg)
	if err != nil {
		_ = conn.CloseWithError(1, "handshake failed")
		return nil, err
	}
	return s, nil
}

func clientHandshake(ctx context.Context, conn *quic.Conn, cfg ClientConfig) (*Session, error) {
	ctrl, err := conn.OpenStreamSync(ctx)
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { ctrl.CancelRead(0); ctrl.CancelWrite(0) })
	defer stop()

	if err := wire.WriteMessage(ctrl, &wire.Hello{Version: wire.Version, Role: wire.RoleClient, Name: cfg.Name}); err != nil {
		return nil, err
	}
	var hello *wire.Hello
	if err := readInto(ctrl, &hello); err != nil {
		return nil, err
	}
	if hello.Version != wire.Version || hello.Role != wire.RoleHost {
		return nil, fmt.Errorf("transport: host hello version %d role %d", hello.Version, hello.Role)
	}

	tlsState := conn.ConnectionState().TLS
	export := tlsState.ExportKeyingMaterial
	req := wire.AuthRequest{Method: cfg.Auth, DeviceName: cfg.Name}
	if cfg.Auth == wire.AuthPIN {
		if req.PINProof, err = pinProof(export, cfg.PIN, wire.RoleClient); err != nil {
			return nil, err
		}
	}
	if err := wire.WriteMessage(ctrl, &req); err != nil {
		return nil, err
	}
	var resp *wire.AuthResponse
	if err := readInto(ctrl, &resp); err != nil {
		return nil, err
	}
	if resp.Status != wire.AuthOK {
		return nil, fmt.Errorf("%w (status %d): %s", ErrAuthDenied, resp.Status, resp.Message)
	}
	if cfg.Auth == wire.AuthPIN {
		want, err := pinProof(export, cfg.PIN, wire.RoleHost)
		if err != nil {
			return nil, err
		}
		if !proofEqual(want, resp.PINProof) {
			return nil, ErrHostPINMismatch
		}
	}

	if err := wire.WriteMessage(ctrl, &cfg.Session); err != nil {
		return nil, err
	}
	var ready *wire.SessionReady
	if err := readInto(ctrl, &ready); err != nil {
		return nil, err
	}
	if ready.ShardSize < 64 || int(ready.ShardSize)+wire.VideoHeaderLen+AEADOverhead > 65507 {
		return nil, fmt.Errorf("transport: host sent shard size %d", ready.ShardSize)
	}

	hostAddr := conn.RemoteAddr().(*net.UDPAddr).AddrPort()
	peer := netip.AddrPortFrom(hostAddr.Addr().Unmap(), ready.MediaPort)
	udp, err := net.ListenUDP("udp", nil)
	if err != nil {
		return nil, err
	}
	var hostFP [32]byte
	if certs := conn.ConnectionState().TLS.PeerCertificates; len(certs) > 0 {
		hostFP = Fingerprint(certs[0].Raw)
	}
	s, err := newSession(sessionParams{
		isHost:       false,
		conn:         conn,
		ctrl:         ctrl,
		ready:        *ready,
		maxDgram:     int(ready.ShardSize) + wire.VideoHeaderLen + AEADOverhead,
		identity:     resp.Identity,
		peerFP:       hostFP,
		handlers:     cfg.Handlers,
		udp:          udp,
		peer:         peer,
		pingInterval: cfg.PingInterval,
		simLoss:      cfg.SimulateLoss,
	})
	if err != nil {
		udp.Close()
		return nil, err
	}
	stop()
	s.start()
	if err := s.waitMedia(time.Until(deadline(ctx))); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}
