package transport

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"time"

	"github.com/planckdotgg/planck/pkg/wire"
	"github.com/quic-go/quic-go"
)

var ErrHostPINMismatch = errors.New("transport: host could not prove the pairing PIN")

// ErrUnanswered is a Dial whose QUIC handshake timed out: nothing answered
// on that address, and another route to the host may.
var ErrUnanswered = errors.New("transport: the host did not answer")

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
	// LANPath says the host was reached LAN direct. The client then offers
	// wire.MaxLANDatagram when its route to the host has a 1500-byte MTU.
	LANPath bool
	// Capabilities, when set, offers wire.FeatureCapabilities: this JSON
	// goes to a host that offers it too, and the host's Capabilities and
	// Profile come back (Session.PeerCapabilities, Session.Profile).
	Capabilities []byte
	// StreamSwitch offers wire.FeatureStreamSwitch: the Control handler
	// takes wire.Stream and moves the session with SetStream.
	StreamSwitch bool
	// CursorShape offers wire.FeatureCursorShape: the Control handler
	// takes wire.CursorShape.
	CursorShape bool
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
	outer := ctx
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := quic.DialAddr(ctx, addr, tlsConf, controlQUICConfig())
	if err != nil {
		var idle *quic.IdleTimeoutError
		var hs *quic.HandshakeTimeoutError
		attemptTimedOut := errors.Is(err, context.DeadlineExceeded) && outer.Err() == nil
		if errors.As(err, &idle) || errors.As(err, &hs) || attemptTimedOut {
			return nil, fmt.Errorf("%w at %s: %w", ErrUnanswered, addr, err)
		}
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

	features := cfg.Handlers.features() | wire.FeatureHostStats | wire.FeatureCursorState
	if cfg.LANPath && lanDatagramOK(conn.RemoteAddr().(*net.UDPAddr).AddrPort().Addr()) {
		features |= wire.FeatureLANDatagram
	}
	if cfg.Capabilities != nil {
		features |= wire.FeatureCapabilities
	}
	if cfg.StreamSwitch {
		features |= wire.FeatureStreamSwitch
	}
	if cfg.CursorShape {
		features |= wire.FeatureCursorShape
	}
	if err := wire.WriteMessage(ctrl, &wire.Hello{Version: wire.Version, Role: wire.RoleClient, Name: cfg.Name, Features: features}); err != nil {
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

	withCaps := features&hello.Features&wire.FeatureCapabilities != 0
	var hostCaps, profile []byte
	if withCaps {
		if err := wire.WriteMessage(ctrl, &wire.Capabilities{Role: wire.RoleClient, JSON: cfg.Capabilities}); err != nil {
			return nil, err
		}
		var caps *wire.Capabilities
		if err := readInto(ctrl, &caps); err != nil {
			return nil, err
		}
		hostCaps = caps.JSON
	}
	if err := wire.WriteMessage(ctrl, &cfg.Session); err != nil {
		return nil, err
	}
	var ready *wire.SessionReady
	if err := readInto(ctrl, &ready); err != nil {
		return nil, err
	}
	if withCaps {
		var p *wire.Profile
		if err := readInto(ctrl, &p); err != nil {
			return nil, err
		}
		profile = p.JSON
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
		peerFeatures: hello.Features,
		peerName:     hello.Name,
		peerCaps:     hostCaps,
		profile:      profile,
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
