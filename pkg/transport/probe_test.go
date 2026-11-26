package transport

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/planckdotgg/planck/pkg/wire"
)

func calibrationPair(t *testing.T) pair {
	return newPair(t, pairOpts{
		server: func(c *ServerConfig) { c.Calibration = true },
		client: func(c *ClientConfig) { c.Calibration = true },
	})
}

// TestProbeTrain: a back-to-back train arrives whole over loopback, and a
// paced one takes the time its rate gives it.
func TestProbeTrain(t *testing.T) {
	p := calibrationPair(t)
	if p.client.PeerFeatures()&wire.FeatureCalibration == 0 {
		t.Fatal("host did not offer FeatureCalibration")
	}
	size := p.client.MaxDatagram()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	burst, err := p.client.Probe(ctx, ProbeSpec{Count: 64, Size: size})
	if err != nil {
		t.Fatal(err)
	}
	if burst.Sent != 64 || burst.Received() != 64 || burst.Prefix() != 64 {
		t.Fatalf("burst: sent %d, received %d, prefix %d of 64", burst.Sent, burst.Received(), burst.Prefix())
	}
	// 100 datagrams at 20 Mbit/s take about 46 ms to leave.
	paced, err := p.client.Probe(ctx, ProbeSpec{Count: 100, Size: size, RateKbps: 20_000})
	if err != nil {
		t.Fatal(err)
	}
	want := time.Duration(float64(99*size*8) / 20e6 * float64(time.Second))
	if got := time.Duration(paced.SendNs); got < want*8/10 {
		t.Fatalf("paced train left in %v, want about %v", got, want)
	}
	if paced.Received() < 95 {
		t.Fatalf("paced train: %d of 100 arrived", paced.Received())
	}
}

// TestProbeRefusals: the host refuses what it cannot send, and a session
// to a host without the feature refuses to probe.
func TestProbeRefusals(t *testing.T) {
	p := calibrationPair(t)
	ctx := context.Background()
	if _, err := p.client.Probe(ctx, ProbeSpec{Count: 1, Size: p.client.MaxDatagram() + 1}); err == nil {
		t.Fatal("a probe over the datagram size went out")
	}
	if _, err := p.host.Probe(ctx, ProbeSpec{Count: 1, Size: 100}); !errors.Is(err, ErrWrongRole) {
		t.Fatalf("host probe: %v", err)
	}
	plain := newPair(t, pairOpts{})
	if _, err := plain.client.Probe(ctx, ProbeSpec{Count: 1, Size: 100}); !errors.Is(err, ErrPeerUnsupported) {
		t.Fatalf("probe to a host without calibration: %v", err)
	}
}

// TestProbeCancel: a cancelled probe returns at once and leaves the
// session free for the next one.
func TestProbeCancel(t *testing.T) {
	p := calibrationPair(t)
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		// 20000 datagrams at 10 Mbit/s would take over 20 s.
		_, err := p.client.Probe(ctx, ProbeSpec{Count: 20000, Size: p.client.MaxDatagram(), RateKbps: 10_000})
		errc <- err
	}()
	time.Sleep(50 * time.Millisecond)
	start := time.Now()
	cancel()
	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled probe: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled probe did not return")
	}
	if d := time.Since(start); d > 200*time.Millisecond {
		t.Fatalf("cancel took %v", d)
	}
	if p.client.probe.Load() != nil {
		t.Fatal("cancelled probe left its collector")
	}
}

// TestCalibrationNeedsTheHostFeature: a calibration dial to an older host
// fails with ErrCalibrationUnsupported, and no session starts.
func TestCalibrationNeedsTheHostFeature(t *testing.T) {
	hostCert, clientCert := identities(t)
	sv := startServer(t, pinServerConfig(hostCert))
	accepted := make(chan error, 1)
	go func() {
		s, err := sv.Accept(context.Background())
		if s != nil {
			s.Close()
		}
		accepted <- err
	}()
	_, err := Dial(context.Background(), sv.Addr().String(), ClientConfig{
		Certificate: clientCert, Name: "mac", VerifyHost: PinnedHost(CertFingerprint(hostCert)),
		Auth: wire.AuthPIN, PIN: testPIN, Calibration: true,
	})
	if !errors.Is(err, ErrCalibrationUnsupported) {
		t.Fatalf("dial: %v, want ErrCalibrationUnsupported", err)
	}
	select {
	case err := <-accepted:
		if err == nil {
			t.Fatal("the host started a session")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the host's handshake did not end")
	}
}

func TestPing(t *testing.T) {
	p := calibrationPair(t)
	rtts, err := p.client.Ping(context.Background(), 20, 5*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if len(rtts) < 15 {
		t.Fatalf("%d of 20 pongs", len(rtts))
	}
	for _, r := range rtts {
		if r <= 0 || r > 100*time.Millisecond {
			t.Fatalf("loopback round trip %v", r)
		}
	}
}
