package tailscale

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const fakeStatus = `{
  "BackendState": "Running",
  "Self": {"ID": "nSELF", "HostName": "mac", "DNSName": "mac.tail.ts.net.", "OS": "macOS",
           "UserID": 42, "TailscaleIPs": ["100.74.237.90", "fd7a:115c:a1e0::1"], "Online": true, "Relay": "den"},
  "Peer": {
    "nodekey:a": {"ID": "nBIG", "HostName": "bigboi", "DNSName": "bigboi.tail.ts.net.", "OS": "windows",
                  "UserID": 42, "TailscaleIPs": ["100.104.23.37"], "Online": true,
                  "CurAddr": "192.168.188.126:41641", "Relay": "den"},
    "nodekey:b": {"ID": "nFAR", "HostName": "laptop", "DNSName": "laptop.tail.ts.net.", "OS": "linux",
                  "UserID": 7, "TailscaleIPs": ["100.83.24.50"], "Online": true, "CurAddr": "", "Relay": "sea"}
  },
  "User": {"42": {"ID": 42, "LoginName": "chris@example.org"}, "7": {"ID": 7, "LoginName": "guest@example.org"}}
}`

func fakeLocalAPI(t *testing.T, token string, status string) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/localapi/v0/status", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(status))
	})
	mux.HandleFunc("/localapi/v0/whois", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("addr") {
		case "100.104.23.37:5000", "100.104.23.37":
			w.Write([]byte(`{"Node":{"StableID":"nBIG","Name":"bigboi.tail.ts.net.","ComputedName":"bigboi","Hostinfo":{"Hostname":"bigboi"}},
				"UserProfile":{"ID":42,"LoginName":"chris@example.org"}}`))
		case "100.83.24.50:5000":
			w.Write([]byte(`{"Node":{"StableID":"nFAR","Name":"laptop.tail.ts.net.","Hostinfo":{"Hostname":"laptop"}},
				"UserProfile":{"ID":7,"LoginName":"guest@example.org"}}`))
		default:
			http.Error(w, "no match for IP:port", http.StatusNotFound)
		}
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "local-tailscaled.sock" {
			http.Error(w, "bad host", http.StatusForbidden)
			return
		}
		if token != "" {
			if _, pass, ok := r.BasicAuth(); !ok || pass != token {
				http.Error(w, "auth required", http.StatusUnauthorized)
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}

func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "pts")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func unixServer(t *testing.T, h http.Handler) *Client {
	t.Helper()
	sock := filepath.Join(shortTempDir(t), "ts.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(h)
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)
	return NewUnix(sock)
}

func tokenServer(t *testing.T, token, sendToken string) *Client {
	t.Helper()
	srv := httptest.NewServer(fakeLocalAPI(t, token, fakeStatus))
	t.Cleanup(srv.Close)
	addr := srv.Listener.Addr().String()
	return &Client{
		resolve: func() (endpoint, error) { return endpoint{network: "tcp", address: addr, token: sendToken}, nil },
		dial:    dialEndpoint,
	}
}

func TestStatusOverUnixSocket(t *testing.T) {
	c := unixServer(t, fakeLocalAPI(t, "", fakeStatus))
	st, err := c.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Self.ID != "nSELF" || st.Self.UserLogin != "chris@example.org" || len(st.Self.TailscaleIPs) != 2 {
		t.Fatalf("self = %+v", st.Self)
	}
	if len(st.Peers) != 2 {
		t.Fatalf("peers = %d, want 2", len(st.Peers))
	}
	big, far := st.Peers[0], st.Peers[1]
	if big.HostName != "bigboi" || !big.Direct || big.CurAddr != "192.168.188.126:41641" || big.TailscaleIPs[0] != netip.MustParseAddr("100.104.23.37") {
		t.Fatalf("bigboi = %+v", big)
	}
	if far.HostName != "laptop" || far.Direct || far.Relay != "sea" || far.UserLogin != "guest@example.org" {
		t.Fatalf("laptop = %+v", far)
	}
}

func TestTokenAuth(t *testing.T) {
	if _, err := tokenServer(t, "s3cret", "s3cret").Status(context.Background()); err != nil {
		t.Fatalf("with token: %v", err)
	}
	_, err := tokenServer(t, "s3cret", "").Status(context.Background())
	var he *httpError
	if !errors.As(err, &he) || he.code != http.StatusUnauthorized {
		t.Fatalf("without token: err = %v, want HTTP 401", err)
	}
}

func TestWhoIs(t *testing.T) {
	c := unixServer(t, fakeLocalAPI(t, "", fakeStatus))
	ctx := context.Background()

	id, err := c.WhoIs(ctx, "100.104.23.37:5000")
	if err != nil {
		t.Fatal(err)
	}
	if !id.IsSameUser || id.NodeID != "nBIG" || id.HostName != "bigboi" || id.LoginName != "chris@example.org" || id.UserID != 42 {
		t.Fatalf("same user whois = %+v", id)
	}

	id, err = c.WhoIs(ctx, "100.83.24.50:5000")
	if err != nil {
		t.Fatal(err)
	}
	if id.IsSameUser || id.LoginName != "guest@example.org" {
		t.Fatalf("other user whois = %+v", id)
	}

	if _, err := c.WhoIs(ctx, "100.99.99.99:5000"); !errors.Is(err, ErrPeerNotFound) {
		t.Fatalf("unknown peer: err = %v, want ErrPeerNotFound", err)
	}
	if _, err := c.WhoIs(ctx, "not-an-ip"); err == nil {
		t.Fatal("invalid address accepted")
	}
}

func TestPathInfo(t *testing.T) {
	c := unixServer(t, fakeLocalAPI(t, "", fakeStatus))
	ctx := context.Background()

	p, err := c.PathInfo(ctx, netip.MustParseAddr("100.104.23.37"))
	if err != nil {
		t.Fatal(err)
	}
	if want := (PathInfo{Direct: true, Relay: "den", MTU: 1280, RecommendedMaxBitrateKbps: 100_000}); p != want {
		t.Fatalf("direct path = %+v, want %+v", p, want)
	}

	p, err = c.PathInfo(ctx, netip.MustParseAddr("::ffff:100.83.24.50"))
	if err != nil {
		t.Fatal(err)
	}
	if want := (PathInfo{Direct: false, Relay: "sea", MTU: 1280, RecommendedMaxBitrateKbps: 20_000}); p != want {
		t.Fatalf("relay path = %+v, want %+v", p, want)
	}

	if _, err := c.PathInfo(ctx, netip.MustParseAddr("100.1.2.3")); !errors.Is(err, ErrPeerNotFound) {
		t.Fatalf("unknown peer: err = %v", err)
	}
}

func TestNotRunning(t *testing.T) {
	missing := NewUnix(filepath.Join(shortTempDir(t), "absent.sock"))
	if _, err := missing.Status(context.Background()); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("missing socket: err = %v, want ErrNotRunning", err)
	}

	stopped := unixServer(t, fakeLocalAPI(t, "", `{"BackendState":"Stopped","Self":{"ID":"x"}}`))
	if _, err := stopped.Status(context.Background()); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("stopped backend: err = %v, want ErrNotRunning", err)
	}
	if _, err := stopped.WhoIs(context.Background(), "100.104.23.37"); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("whois on stopped backend: err = %v, want ErrNotRunning", err)
	}
}

func TestContextCancelsHungDaemon(t *testing.T) {
	release := make(chan struct{})
	c := unixServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	defer close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.Status(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("Status took %v after the context ended", time.Since(start))
	}
}

func TestLiveDaemon(t *testing.T) {
	if os.Getenv("PLANCK_TS_LIVE") == "" {
		t.Skip("set PLANCK_TS_LIVE=1 to query the real Tailscale daemon")
	}
	c := New()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st, err := c.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("self %s %s %v user=%s peers=%d", st.Self.ID, st.Self.DNSName, st.Self.TailscaleIPs, st.Self.UserLogin, len(st.Peers))
	for _, p := range st.Peers {
		if !p.Online || p.UserID != st.Self.UserID || len(p.TailscaleIPs) == 0 {
			continue
		}
		id, err := c.WhoIs(ctx, p.TailscaleIPs[0].String())
		if err != nil {
			t.Fatalf("whois %s: %v", p.HostName, err)
		}
		path, err := c.PathInfo(ctx, p.TailscaleIPs[0])
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("peer %s: sameUser=%v login=%s path=%+v", id.HostName, id.IsSameUser, id.LoginName, path)
		if !id.IsSameUser {
			t.Fatalf("own device %s not reported as same user", p.HostName)
		}
	}
}
