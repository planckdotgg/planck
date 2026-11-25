package main

import (
	"bytes"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/planckdotgg/planck/internal/session"
	"github.com/planckdotgg/planck/pkg/audio"
	"github.com/planckdotgg/planck/pkg/defaults"
)

type fakeClock struct{ ns atomic.Int64 }

func (c *fakeClock) now() time.Time          { return time.Unix(0, c.ns.Load()) }
func (c *fakeClock) advance(d time.Duration) { c.ns.Add(int64(d)) }

// TestProbesLoadOncePerTTL: Settings asks every frame, the machine is
// probed once.
func TestProbesLoadOncePerTTL(t *testing.T) {
	var displays, outputs, inputs, apps atomic.Int32
	b, err := newBackend(backendConfig{dir: t.TempDir(), finder: newFinder("", nil, nil, t.Logf), logf: t.Logf,
		displays: func() []defaults.HostDisplay {
			displays.Add(1)
			return []defaults.HostDisplay{{ID: "1", Width: 1920, Height: 1080, RefreshHz: 60, Primary: true}}
		},
		audioOutputs: func() ([]audio.Device, error) {
			outputs.Add(1)
			return []audio.Device{{ID: "o1", Name: "Speakers"}}, nil
		},
		audioInputs: func() ([]audio.Device, error) { inputs.Add(1); return nil, nil },
		runningApps: func() ([]audio.App, error) {
			apps.Add(1)
			return []audio.App{{PID: 1, Name: "Discord"}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for range 100 {
		d := b.Displays()
		d[0].ID = "mutated"
		a := b.AudioDevices()
		a.Outputs[0].Name = "mutated"
		r := b.RunningApps()
		r[0] = "mutated"
	}
	if d := b.Displays(); d[0].ID != "1" {
		t.Errorf("a caller changed the cached displays: %+v", d)
	}
	if a := b.AudioDevices(); a.Outputs[0].Name != "Speakers" {
		t.Errorf("a caller changed the cached devices: %+v", a)
	}
	if r := b.RunningApps(); r[0] != "Discord" {
		t.Errorf("a caller changed the cached apps: %v", r)
	}
	for name, n := range map[string]int32{"displays": displays.Load(), "outputs": outputs.Load(), "inputs": inputs.Load(), "apps": apps.Load()} {
		if n != 1 {
			t.Errorf("%s probed %d times, want 1", name, n)
		}
	}
}

// TestStaleDisplaysRefreshInBackground: past the TTL, Displays answers at
// once from the cache while a slow probe runs, then serves its answer.
func TestStaleDisplaysRefreshInBackground(t *testing.T) {
	var probes atomic.Int32
	release := make(chan struct{})
	b, err := newBackend(backendConfig{dir: t.TempDir(), finder: newFinder("", nil, nil, t.Logf), logf: t.Logf,
		displays: func() []defaults.HostDisplay {
			if probes.Add(1) == 1 {
				return []defaults.HostDisplay{{ID: "1", Width: 1920, Height: 1080, RefreshHz: 60}}
			}
			<-release
			return []defaults.HostDisplay{{ID: "2", Width: 3840, Height: 2160, RefreshHz: 120}}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var clock fakeClock
	b.caches.displays.now = clock.now
	var changes atomic.Int32
	b.Subscribe(func() { changes.Add(1) })

	if d := b.Displays(); d[0].ID != "1" {
		t.Fatalf("first displays %+v", d)
	}
	clock.advance(displaysTTL)
	done := make(chan []string)
	go func() {
		var ids []string
		for range 10 {
			ids = append(ids, b.Displays()[0].ID)
		}
		done <- ids
	}()
	select {
	case ids := <-done:
		for _, id := range ids {
			if id != "1" {
				t.Fatalf("stale displays %v, want the cached 1", ids)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Displays blocked on the slow probe")
	}
	waitUntil(t, "the background probe to start", func() bool { return probes.Load() == 2 })
	if n := changes.Load(); n != 0 {
		t.Fatalf("%d changes before the probe finished", n)
	}
	close(release)
	waitUntil(t, "the refreshed displays", func() bool { return changes.Load() == 1 && b.Displays()[0].ID == "2" })
	if n := probes.Load(); n != 2 {
		t.Errorf("probed %d times, want 2", n)
	}
}

// TestPairedClientsWhileNotHostingReadsChangedFileOnly: an unchanged
// paired-clients.json is not read again; a revoke or an edit shows at once.
func TestPairedClientsWhileNotHostingReadsChangedFileOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, session.PairedClientsFile)
	fpA, fpB := string(bytes.Repeat([]byte("a"), 64)), string(bytes.Repeat([]byte("b"), 64))
	write := func(body string, mod time.Time) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, mod, mod); err != nil {
			t.Fatal(err)
		}
	}
	mod := time.Unix(1_800_000_000, 0)
	two := `{"` + fpA + `":{"name":"mac","method":"pin","pairedAt":"2026-11-01T00:00:00Z"},"` +
		fpB + `":{"name":"pc","method":"pin","pairedAt":"2026-11-02T00:00:00Z"}}`
	write(two, mod)
	b, _ := newTestBackend(t, dir, nil)
	if pc := b.PairedClients(); len(pc) != 2 {
		t.Fatalf("paired %+v", pc)
	}

	// Same size and time, but no longer JSON: a re-read would fail.
	write(string(bytes.Repeat([]byte("x"), len(two))), mod)
	for range 100 {
		if pc := b.PairedClients(); len(pc) != 2 {
			t.Fatalf("re-read an unchanged file: %+v", pc)
		}
	}

	write(two, mod)
	if err := b.RevokeClient(fpB); err != nil {
		t.Fatal(err)
	}
	if pc := b.PairedClients(); len(pc) != 1 || pc[0].Fingerprint != fpA {
		t.Fatalf("after revoke %+v", pc)
	}

	write(`{"`+fpB+`":{"name":"laptop","method":"pin","pairedAt":"2026-11-03T00:00:00Z"}}`, mod.Add(time.Hour))
	if pc := b.PairedClients(); len(pc) != 1 || pc[0].Name != "laptop" {
		t.Fatalf("after an edit %+v", pc)
	}
}
