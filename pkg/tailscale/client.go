// Package tailscale is a small client for the local Tailscale daemon's
// LocalAPI. It speaks HTTP/1.1 directly over the daemon's socket, named pipe,
// or authenticated localhost port, so Planck does not depend on tailscale.com.
package tailscale

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
)

var (
	// ErrNotRunning means no Tailscale daemon answered, or it is not in the
	// Running state (stopped, logged out, or waiting for auth).
	ErrNotRunning = errors.New("tailscale: not running")
	// ErrPeerNotFound means the address does not belong to a known tailnet node.
	ErrPeerNotFound = errors.New("tailscale: peer not found")
)

const (
	// TunnelMTU is the MTU Tailscale gives its TUN device; media packets on a
	// tailnet path must fit inside it.
	TunnelMTU = 1280

	DirectMaxBitrateKbps = 100_000
	RelayMaxBitrateKbps  = 20_000

	maxResponseBytes = 32 << 20
)

// endpoint is one way to reach the daemon. token, when set, is sent as the
// HTTP Basic password, which the macOS GUI daemons require.
type endpoint struct {
	network string
	address string
	token   string
}

// Client talks to the local Tailscale daemon. The zero value is not usable;
// call New or NewUnix.
type Client struct {
	resolve func() (endpoint, error)
	dial    func(ctx context.Context, ep endpoint) (net.Conn, error)
	// cli, when set, reads the status from the tailscale command when the
	// LocalAPI cannot be reached.
	cli func(ctx context.Context) ([]byte, error)
}

// New returns a client for the platform's standard daemon location. The
// location is resolved on every call, so a daemon restart on a new port is
// picked up.
func New() *Client {
	return &Client{resolve: platformEndpoint, dial: dialEndpoint, cli: statusFromCLI}
}

// statusFromCLI runs "tailscale status --json", whose output is the
// LocalAPI's status.
func statusFromCLI(ctx context.Context) ([]byte, error) {
	bins := []string{"tailscale"}
	switch runtime.GOOS {
	case "darwin":
		bins = append(bins, "/Applications/Tailscale.app/Contents/MacOS/Tailscale")
	case "windows":
		bins = append(bins, `C:\Program Files\Tailscale\tailscale.exe`)
	}
	var err error
	for _, bin := range bins {
		var out []byte
		if out, err = exec.CommandContext(ctx, bin, "status", "--json").Output(); err == nil {
			return out, nil
		}
	}
	return nil, err
}

// NewUnix returns a client for a tailscaled started with a custom --socket.
func NewUnix(socketPath string) *Client {
	return &Client{
		resolve: func() (endpoint, error) { return endpoint{network: "unix", address: socketPath}, nil },
		dial:    dialEndpoint,
	}
}

func dialEndpoint(ctx context.Context, ep endpoint) (net.Conn, error) {
	if ep.network == "pipe" {
		return dialPipe(ctx, ep.address)
	}
	var d net.Dialer
	return d.DialContext(ctx, ep.network, ep.address)
}

// Node is one tailnet device as seen from this machine.
type Node struct {
	ID           string
	HostName     string
	DNSName      string
	OS           string
	UserID       int64
	UserLogin    string
	TailscaleIPs []netip.Addr
	Online       bool
	// CurAddr is the peer's current direct UDP endpoint; empty when traffic
	// goes through a relay or no handshake has happened yet.
	CurAddr string
	// Relay is the peer's home DERP region code (for example "den").
	Relay  string
	Direct bool
}

type Status struct {
	BackendState string
	Self         Node
	Peers        []Node
}

type PeerIdentity struct {
	NodeID     string
	HostName   string
	DNSName    string
	UserID     int64
	LoginName  string
	IsSameUser bool
}

type PathInfo struct {
	Direct                    bool
	Relay                     string
	MTU                       int
	RecommendedMaxBitrateKbps int
}

type rawPeer struct {
	ID           string
	HostName     string
	DNSName      string
	OS           string
	UserID       int64
	TailscaleIPs []string
	Online       bool
	CurAddr      string
	Relay        string
}

type rawStatus struct {
	BackendState string
	Self         *rawPeer
	Peer         map[string]*rawPeer
	User         map[string]struct{ LoginName string }
}

type rawWhoIs struct {
	Node struct {
		StableID     string
		Name         string
		ComputedName string
		Hostinfo     struct{ Hostname string }
	}
	UserProfile struct {
		ID        int64
		LoginName string
	}
}

func (c *Client) Status(ctx context.Context) (*Status, error) {
	var raw rawStatus
	err := c.getJSON(ctx, "/localapi/v0/status", &raw)
	if errors.Is(err, ErrNotRunning) && c.cli != nil && ctx.Err() == nil {
		if out, cerr := c.cli(ctx); cerr == nil {
			raw = rawStatus{}
			err = json.Unmarshal(out, &raw)
		}
	}
	if err != nil {
		return nil, err
	}
	if raw.BackendState != "Running" || raw.Self == nil {
		return nil, fmt.Errorf("%w: backend state %q", ErrNotRunning, raw.BackendState)
	}
	logins := make(map[int64]string, len(raw.User))
	for id, u := range raw.User {
		n, err := strconv.ParseInt(id, 10, 64)
		if err == nil {
			logins[n] = u.LoginName
		}
	}
	st := &Status{BackendState: raw.BackendState, Self: raw.Self.node(logins)}
	for _, p := range raw.Peer {
		if p != nil {
			st.Peers = append(st.Peers, p.node(logins))
		}
	}
	sort.Slice(st.Peers, func(i, j int) bool { return st.Peers[i].DNSName < st.Peers[j].DNSName })
	return st, nil
}

func (p *rawPeer) node(logins map[int64]string) Node {
	n := Node{
		ID:        p.ID,
		HostName:  p.HostName,
		DNSName:   p.DNSName,
		OS:        p.OS,
		UserID:    p.UserID,
		UserLogin: logins[p.UserID],
		Online:    p.Online,
		CurAddr:   p.CurAddr,
		Relay:     p.Relay,
		Direct:    p.CurAddr != "",
	}
	for _, s := range p.TailscaleIPs {
		if a, err := netip.ParseAddr(s); err == nil {
			n.TailscaleIPs = append(n.TailscaleIPs, a)
		}
	}
	return n
}

// WhoIs identifies the tailnet node behind remoteAddr ("ip" or "ip:port").
// IsSameUser is true only when the node belongs to the same user as this
// machine; tagged devices never match.
func (c *Client) WhoIs(ctx context.Context, remoteAddr string) (*PeerIdentity, error) {
	if _, err := netip.ParseAddrPort(remoteAddr); err != nil {
		if _, err := netip.ParseAddr(remoteAddr); err != nil {
			return nil, fmt.Errorf("tailscale: whois: invalid address %q", remoteAddr)
		}
	}
	st, err := c.Status(ctx)
	if err != nil {
		return nil, err
	}
	var raw rawWhoIs
	if err := c.getJSON(ctx, "/localapi/v0/whois?addr="+url.QueryEscape(remoteAddr), &raw); err != nil {
		var he *httpError
		if errors.As(err, &he) && he.code == http.StatusNotFound {
			return nil, fmt.Errorf("%w: %s", ErrPeerNotFound, remoteAddr)
		}
		return nil, err
	}
	host := raw.Node.Hostinfo.Hostname
	if host == "" {
		host = raw.Node.ComputedName
	}
	return &PeerIdentity{
		NodeID:     raw.Node.StableID,
		HostName:   host,
		DNSName:    raw.Node.Name,
		UserID:     raw.UserProfile.ID,
		LoginName:  raw.UserProfile.LoginName,
		IsSameUser: raw.UserProfile.ID != 0 && raw.UserProfile.ID == st.Self.UserID,
	}, nil
}

// PathInfo reports how traffic to the peer owning peerIP currently flows.
func (c *Client) PathInfo(ctx context.Context, peerIP netip.Addr) (PathInfo, error) {
	st, err := c.Status(ctx)
	if err != nil {
		return PathInfo{}, err
	}
	peerIP = peerIP.Unmap()
	for _, p := range st.Peers {
		for _, ip := range p.TailscaleIPs {
			if ip == peerIP {
				return pathFor(p), nil
			}
		}
	}
	return PathInfo{}, fmt.Errorf("%w: %s", ErrPeerNotFound, peerIP)
}

// pathFor derives path facts from a peer already read from Status.
func pathFor(p Node) PathInfo {
	info := PathInfo{Direct: p.Direct, Relay: p.Relay, MTU: TunnelMTU, RecommendedMaxBitrateKbps: RelayMaxBitrateKbps}
	if p.Direct {
		info.RecommendedMaxBitrateKbps = DirectMaxBitrateKbps
	}
	return info
}

func (c *Client) getJSON(ctx context.Context, path string, v any) error {
	ep, err := c.resolve()
	if err != nil {
		return err
	}
	conn, err := c.dial(ctx, ep)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNotRunning, err)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://local-tailscaled.sock"+path, nil)
	if err != nil {
		return err
	}
	req.Close = true
	if ep.token != "" {
		req.SetBasicAuth("", ep.token)
	}
	if err := req.Write(conn); err != nil {
		return ctxErr(ctx, fmt.Errorf("tailscale: write request: %w", err))
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		return ctxErr(ctx, fmt.Errorf("tailscale: read response: %w", err))
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return ctxErr(ctx, fmt.Errorf("tailscale: read body: %w", err))
	}
	if resp.StatusCode != http.StatusOK {
		return &httpError{path: path, code: resp.StatusCode, body: bytesTrim(body)}
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("tailscale: decode %s: %w", path, err)
	}
	return nil
}

type httpError struct {
	path string
	code int
	body string
}

func (e *httpError) Error() string {
	return fmt.Sprintf("tailscale: %s: HTTP %d: %s", e.path, e.code, e.body)
}

func ctxErr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func bytesTrim(b []byte) string {
	const max = 200
	if len(b) > max {
		b = b[:max]
	}
	return string(b)
}
