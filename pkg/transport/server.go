package transport

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/planckdotgg/planck/pkg/fec"
	"github.com/planckdotgg/planck/pkg/ratectl"
	"github.com/planckdotgg/planck/pkg/wire"
)

const defaultHandshakeTimeout = 10 * time.Second

var ErrAuthDenied = errors.New("transport: authentication denied")

// AuthInfo is what the host knows about a connecting client when it decides
// whether to let it stream.
type AuthInfo struct {
	Method            wire.AuthMethod
	DeviceName        string
	ClientFingerprint [32]byte
	RemoteAddr        netip.AddrPort
	// PINVerified is true when Method is AuthPIN and the client proved it
	// knows the host's current PIN on this TLS session.
	PINVerified bool
}

type ServerConfig struct {
	Certificate tls.Certificate
	Name        string
	// Authorize returns the client's identity, or an error to refuse it.
	// A host typically accepts a stored fingerprint for AuthPaired, stores
	// the fingerprint when PINVerified, and asks Tailscale WhoIs about
	// RemoteAddr for AuthTailscale. Required.
	Authorize func(ctx context.Context, info AuthInfo) (identity string, err error)
	// PIN returns the pairing PIN currently shown to the user, or "" when
	// pairing is closed.
	PIN func() string
	// Negotiate turns the client's requested config into the accepted one.
	// Nil accepts the request unchanged.
	Negotiate func(req wire.SessionConfig) (wire.SessionConfig, error)
	// Capabilities, when set, offers wire.FeatureCapabilities. With a
	// client that offers it too, the host sends this JSON after AuthOK,
	// reads the client's, and settles the session with Resolve instead of
	// Negotiate.
	Capabilities func() []byte
	Resolve      func(ResolveRequest) (ResolveResult, error)
	// MediaAddr is the local address for media sockets; default ":0".
	MediaAddr string
	// MaxDatagram caps sealed media datagrams. Zero takes
	// wire.MaxLANDatagram on a LAN path both ends route over a 1500-byte
	// MTU (the client says so with wire.FeatureLANDatagram), and
	// wire.MaxDatagram otherwise.
	MaxDatagram int
	// Rate configures the bitrate controller. Zero fields take
	// ratectl.DefaultConfig values; MaxKbps is further capped by the
	// session's BitrateKbps.
	Rate ratectl.Config
	// Path overrides the path guessed from the client's address.
	Path ratectl.Path
	// FECMinLoss keeps parity for at least this loss rate on clean paths.
	FECMinLoss       float64
	Handlers         Handlers
	PingInterval     time.Duration
	HandshakeTimeout time.Duration
	// SimulateLoss drops this fraction of outgoing media datagrams, for
	// tests and diagnostics.
	SimulateLoss float64
}

// Server accepts client sessions on a QUIC control listener.
type Server struct {
	cfg ServerConfig
	ln  *quic.Listener
}

// Listen starts a host control listener on addr.
func Listen(addr string, cfg ServerConfig) (*Server, error) {
	if cfg.Authorize == nil {
		return nil, errors.New("transport: ServerConfig.Authorize is required")
	}
	if len(cfg.Certificate.Certificate) == 0 {
		return nil, errors.New("transport: ServerConfig.Certificate is required")
	}
	ln, err := quic.ListenAddr(addr, ServerTLSConfig(cfg.Certificate), controlQUICConfig())
	if err != nil {
		return nil, err
	}
	return &Server{cfg: cfg, ln: ln}, nil
}

func (sv *Server) Addr() net.Addr { return sv.ln.Addr() }
func (sv *Server) Close() error   { return sv.ln.Close() }

// HandshakeError is a failed handshake of one connection; the server keeps
// listening.
type HandshakeError struct {
	Remote net.Addr
	Err    error
}

func (e *HandshakeError) Error() string {
	return fmt.Sprintf("transport: handshake with %v: %v", e.Remote, e.Err)
}
func (e *HandshakeError) Unwrap() error { return e.Err }

// Accept waits for the next connection and runs its handshake. A failed
// handshake returns a *HandshakeError; any other error means the listener
// is done.
func (sv *Server) Accept(ctx context.Context) (*Session, error) {
	conn, err := sv.ln.Accept(ctx)
	if err != nil {
		return nil, err
	}
	s, err := sv.handshake(ctx, conn)
	if err != nil {
		go lingerClose(conn)
		return nil, &HandshakeError{Remote: conn.RemoteAddr(), Err: err}
	}
	return s, nil
}

func (sv *Server) handshake(parent context.Context, conn *quic.Conn) (*Session, error) {
	timeout := sv.cfg.HandshakeTimeout
	if timeout <= 0 {
		timeout = defaultHandshakeTimeout
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	ctrl, err := conn.AcceptStream(ctx)
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { ctrl.CancelRead(0); ctrl.CancelWrite(0) })
	defer stop()

	var hello *wire.Hello
	if err := readInto(ctrl, &hello); err != nil {
		return nil, err
	}
	if hello.Version != wire.Version || hello.Role != wire.RoleClient {
		_ = wire.WriteMessage(ctrl, &wire.Goodbye{Reason: wire.GoodbyeProtocolError, Message: "unsupported version"})
		return nil, fmt.Errorf("client hello version %d role %d", hello.Version, hello.Role)
	}
	features := wire.FeatureInputDedup
	if sv.cfg.Capabilities != nil && sv.cfg.Resolve != nil {
		features |= wire.FeatureCapabilities
	}
	if err := wire.WriteMessage(ctrl, &wire.Hello{Version: wire.Version, Role: wire.RoleHost, Name: sv.cfg.Name, Features: features}); err != nil {
		return nil, err
	}

	identity, fp, err := sv.authenticate(ctx, conn, ctrl)
	if err != nil {
		return nil, err
	}

	withCaps := features&hello.Features&wire.FeatureCapabilities != 0
	var peerCaps []byte
	if withCaps {
		if err := wire.WriteMessage(ctrl, &wire.Capabilities{Role: wire.RoleHost, JSON: sv.cfg.Capabilities()}); err != nil {
			return nil, err
		}
		var caps *wire.Capabilities
		if err := readInto(ctrl, &caps); err != nil {
			return nil, err
		}
		peerCaps = caps.JSON
	}

	var req *wire.SessionConfig
	if err := readInto(ctrl, &req); err != nil {
		return nil, err
	}
	remote := conn.RemoteAddr().(*net.UDPAddr).AddrPort()
	path := sv.cfg.Path
	if path == ratectl.PathUnknown {
		path = ratectl.ClassifyAddr(remote.Addr())
	}
	maxDgram := sv.cfg.MaxDatagram
	if maxDgram <= 0 {
		maxDgram = wire.MaxDatagram
		if hello.Features&wire.FeatureLANDatagram != 0 && path == ratectl.PathLAN && lanDatagramOK(remote.Addr()) {
			maxDgram = wire.MaxLANDatagram
		}
	}
	shard := maxDgram - AEADOverhead - wire.VideoHeaderLen
	if shard < 64 {
		return nil, fmt.Errorf("MaxDatagram %d leaves no room for video shards", maxDgram)
	}
	accepted := *req
	var resolved ResolveResult
	switch {
	case withCaps:
		resolved, err = sv.cfg.Resolve(ResolveRequest{Request: *req, ClientCaps: peerCaps, Path: path,
			LANDatagram: maxDgram == wire.MaxLANDatagram, Remote: remote})
		accepted = resolved.Config
	case sv.cfg.Negotiate != nil:
		accepted, err = sv.cfg.Negotiate(*req)
	}
	if err == nil && accepted.Codec == wire.CodecPyroWave && !withCaps {
		err = errors.New("PyroWave needs a peer with capabilities")
	}
	if err != nil {
		_ = wire.WriteMessage(ctrl, &wire.Goodbye{Reason: wire.GoodbyeProtocolError, Message: err.Error()})
		return nil, err
	}
	mediaAddr := sv.cfg.MediaAddr
	if mediaAddr == "" {
		mediaAddr = ":0"
	}
	ua, err := net.ResolveUDPAddr("udp", mediaAddr)
	if err != nil {
		return nil, err
	}
	udp, err := net.ListenUDP("udp", ua)
	if err != nil {
		return nil, err
	}
	var idb [8]byte
	_, _ = rand.Read(idb[:])
	ready := wire.SessionReady{
		SessionID: binary.BigEndian.Uint64(idb[:]),
		MediaPort: uint16(udp.LocalAddr().(*net.UDPAddr).Port),
		ShardSize: uint16(shard),
		Config:    accepted,
	}

	rate := sv.cfg.Rate
	if resolved.MaxKbps > 0 {
		rate.MaxKbps = resolved.MaxKbps
	}
	if resolved.StartKbps > 0 {
		rate.StartKbps = resolved.StartKbps
	}
	if accepted.BitrateKbps > 0 {
		if rate.MaxKbps <= 0 {
			rate.MaxKbps = ratectl.DefaultConfig().MaxKbps
		}
		rate.MaxKbps = min(rate.MaxKbps, int(accepted.BitrateKbps))
	}
	policy := fec.NewPolicy()
	policy.MinLoss = sv.cfg.FECMinLoss

	s, err := newSession(sessionParams{
		isHost:       true,
		conn:         conn,
		ctrl:         ctrl,
		ready:        ready,
		maxDgram:     maxDgram,
		identity:     identity,
		peerFP:       fp,
		handlers:     sv.cfg.Handlers,
		udp:          udp,
		pingInterval: sv.cfg.PingInterval,
		simLoss:      sv.cfg.SimulateLoss,
		rate:         rate,
		path:         path,
		fecPolicy:    policy,
		parity:       resolved.Parity,
		peerFeatures: hello.Features,
		peerCaps:     peerCaps,
		profile:      resolved.Profile,
	})
	if err != nil {
		udp.Close()
		return nil, err
	}
	if err := wire.WriteMessage(ctrl, &ready); err != nil {
		udp.Close()
		return nil, err
	}
	if withCaps {
		if err := wire.WriteMessage(ctrl, &wire.Profile{JSON: resolved.Profile}); err != nil {
			udp.Close()
			return nil, err
		}
	}
	stop()
	s.start()
	if err := s.waitMedia(time.Until(deadline(ctx))); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

// lingerClose gives the client a moment to read a refusal before the
// connection goes away; closing at once can discard the queued response.
func lingerClose(conn *quic.Conn) {
	select {
	case <-conn.Context().Done():
	case <-time.After(time.Second):
	}
	_ = conn.CloseWithError(1, "handshake failed")
}

func deadline(ctx context.Context) time.Time {
	d, _ := ctx.Deadline()
	return d
}

func (sv *Server) authenticate(ctx context.Context, conn *quic.Conn, ctrl *quic.Stream) (string, [32]byte, error) {
	var fp [32]byte
	var req *wire.AuthRequest
	if err := readInto(ctrl, &req); err != nil {
		return "", fp, err
	}
	tlsState := conn.ConnectionState().TLS
	if len(tlsState.PeerCertificates) == 0 {
		return "", fp, errors.New("client presented no certificate")
	}
	fp = Fingerprint(tlsState.PeerCertificates[0].Raw)
	info := AuthInfo{
		Method:            req.Method,
		DeviceName:        req.DeviceName,
		ClientFingerprint: fp,
		RemoteAddr:        conn.RemoteAddr().(*net.UDPAddr).AddrPort(),
	}
	var resp wire.AuthResponse
	if req.Method == wire.AuthPIN && sv.cfg.PIN != nil {
		if pin := sv.cfg.PIN(); pin != "" {
			want, err := pinProof(tlsState.ExportKeyingMaterial, pin, wire.RoleClient)
			if err != nil {
				return "", fp, err
			}
			info.PINVerified = proofEqual(want, req.PINProof)
			if info.PINVerified {
				if resp.PINProof, err = pinProof(tlsState.ExportKeyingMaterial, pin, wire.RoleHost); err != nil {
					return "", fp, err
				}
			}
		}
	}
	identity, err := sv.cfg.Authorize(ctx, info)
	if err != nil {
		resp = wire.AuthResponse{Status: wire.AuthDenied, Message: err.Error()}
		if req.Method == wire.AuthPaired {
			resp.Status = wire.AuthPINRequired
		}
		_ = wire.WriteMessage(ctrl, &resp)
		return "", fp, fmt.Errorf("%w: %v", ErrAuthDenied, err)
	}
	resp.Status = wire.AuthOK
	resp.Identity = identity
	if err := wire.WriteMessage(ctrl, &resp); err != nil {
		return "", fp, err
	}
	return identity, fp, nil
}

// readInto reads the next control message and requires it to be of the
// type *dst points to.
func readInto[T wire.Message](ctrl *quic.Stream, dst *T) error {
	m, err := wire.ReadMessage(ctrl)
	if err != nil {
		return err
	}
	if bye, ok := m.(*wire.Goodbye); ok {
		return fmt.Errorf("peer said goodbye (%d): %s", bye.Reason, bye.Message)
	}
	v, ok := m.(T)
	if !ok {
		return fmt.Errorf("unexpected %T during handshake", m)
	}
	*dst = v
	return nil
}
