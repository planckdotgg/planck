package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	usage "github.com/planckdotgg/planck/internal/telemetry"
	"github.com/planckdotgg/planck/pkg/config"
)

// usageIngest is a fake TelemetryDeck that keeps every request body.
type usageIngest struct {
	srv    *httptest.Server
	mu     sync.Mutex
	bodies []string
}

func newUsageIngest(t *testing.T) *usageIngest {
	in := &usageIngest{}
	in.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		in.mu.Lock()
		in.bodies = append(in.bodies, string(b))
		in.mu.Unlock()
	}))
	t.Cleanup(in.srv.Close)
	return in
}

func (in *usageIngest) signals(t *testing.T) []usage.Signal {
	in.mu.Lock()
	defer in.mu.Unlock()
	var out []usage.Signal
	for _, b := range in.bodies {
		var batch []usage.Signal
		if err := json.Unmarshal([]byte(b), &batch); err != nil {
			t.Fatalf("body %s: %v", b, err)
		}
		out = append(out, batch...)
	}
	return out
}

func (in *usageIngest) client(dir string, on bool) *usage.Client {
	return usage.New(usage.Options{AppID: "APP", Endpoint: in.srv.URL + "/v2/", TestMode: true, StateFile: filepath.Join(dir, "telemetry.json"),
		AppVersion: "0.1.0", Channel: usage.ChannelDev, Mode: usage.ModeClient, RetryAfter: time.Hour}, on)
}

// TestBackendCountsStreamsWithoutNamingAnything: a stream sends
// session.started and session.ended with its codec and route kind, and no
// signal names the host, this device, an address or the PIN.
func TestBackendCountsStreamsWithoutNamingAnything(t *testing.T) {
	in := newUsageIngest(t)
	h := startFakeHost(t, "off")
	dir := t.TempDir()
	b, _ := newTestBackend(t, dir, nil)
	if !b.Settings().UsageTelemetry {
		t.Fatal("telemetry is off by default")
	}
	b.usageTel = in.client(dir, b.Settings().UsageTelemetry)
	b.usageTel.Launched()
	ctx := context.Background()
	if err := b.Connect(ctx, h.Addr().String()); err == nil {
		t.Fatal("connected unpaired")
	}
	if err := b.SubmitPIN(ctx, "246810"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the stream", func() bool { return b.Telemetry().Connected })
	b.Disconnect()
	b.usageTel.Close(5 * time.Second)

	var types []string
	for _, s := range in.signals(t) {
		types = append(types, s.Type)
		if strings.HasPrefix(s.Type, "session.") {
			if p := s.Payload; p["mode"] != usage.ModeClient || p["transportKind"] != usage.TransportLAN || p["codec"] == "unknown" || p["codec"] == "" {
				t.Errorf("%s payload %v", s.Type, p)
			}
		}
	}
	if want := []string{usage.TypeInstalled, usage.TypeLaunched, usage.TypeSessionStarted, usage.TypeSessionEnded}; strings.Join(types, " ") != strings.Join(want, " ") {
		t.Fatalf("sent %v, want %v", types, want)
	}
	in.mu.Lock()
	all := strings.Join(in.bodies, "")
	in.mu.Unlock()
	for _, secret := range []string{h.Addr().String(), "127.0.0.1", "fake-host", "test-client", "246810", h.HostID(), h.Fingerprint()} {
		if strings.Contains(all, secret) {
			t.Errorf("a signal holds %q", secret)
		}
	}
}

// TestBackendTelemetrySwitch: turning the switch off stops signals at once,
// is kept in client.json, and the next run starts with it off.
func TestBackendTelemetrySwitch(t *testing.T) {
	in := newUsageIngest(t)
	dir := t.TempDir()
	b, _ := newTestBackend(t, dir, nil)
	b.usageTel = in.client(dir, true)
	defer b.usageTel.Close(time.Second)
	s := b.Settings()
	s.UsageTelemetry = false
	if err := b.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	if b.usageTel.Active() {
		t.Fatal("telemetry still sends after the switch went off")
	}
	b.usageTel.Launched()
	if stored, err := config.LoadClientConfig(dir); err != nil || stored.TelemetryOn() {
		t.Fatalf("client.json: on %v, %v", stored.TelemetryOn(), err)
	}
	if again, _ := newTestBackend(t, dir, nil); again.Settings().UsageTelemetry {
		t.Fatal("the next run starts with telemetry on")
	}
	s.UsageTelemetry = true
	if err := b.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	if stored, _ := config.LoadClientConfig(dir); !stored.TelemetryOn() || stored.TelemetryEnabled != nil {
		t.Fatalf("on again: telemetryEnabled %v", stored.TelemetryEnabled)
	}
	if !b.usageTel.Active() {
		t.Fatal("telemetry does not send after the switch went on again")
	}
	b.usageTel.Close(time.Second)
	if sigs := in.signals(t); len(sigs) != 0 {
		t.Fatalf("sent %d signals; app.launched came while the switch was off", len(sigs))
	}
}
