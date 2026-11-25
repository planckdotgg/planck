package ui

import (
	"slices"
	"strings"
	"testing"
	"time"

	mui "github.com/egoist/mygo/ui"
)

func statsTester(t *testing.T, f *fakeBackend) (*App, *mui.Tester) {
	t.Helper()
	a, tt := newTester(t, f)
	a.screen = ScreenStatistics
	tt.Frame()
	return a, tt
}

// TestStatisticsLive: the live page reads the stream's statistics: the
// glass-to-glass figure against the frame budget, the stages, the
// counters a fault turns red, audio, and the route; Copy diagnostics
// copies them, and Disconnect ends the stream.
func TestStatisticsLive(t *testing.T) {
	f := newFake()
	f.stats = liveSample()
	_, tt := statsTester(t, f)
	for _, s := range []string{"bigboi", "8.4", "11.2 ms p99", "budget 8.33 ms at 120 Hz", "over budget",
		"1.60", "1.41", "0.50", "0.87", "4.02", "= 8.40", "1920×1080 @ 120 Hz", "HEVC 4:4:4 10-bit", "RTX 4080",
		"84.2", "cap 150", "1 unrepaired", "412", "18 %", "Opus 48 kHz stereo", "TONOR G11 · live", "direct to display",
		"LAN direct · 1452 B datagrams · AES-128-GCM · slices 4 · NVENC split on · VSync off"} {
		if !tt.HasText(s) {
			t.Errorf("live page misses %q", s)
		}
	}
	if !slices.ContainsFunc(tt.Texts(), func(s string) bool { return strings.HasPrefix(s, "STREAMING 00:4") }) {
		t.Errorf("no session clock: %q", tt.Texts())
	}
	snapshot(t, tt, "statistics-live")

	f.set(func(f *fakeBackend) { f.stats.Total.P50 = 7.9 })
	tt.Frame()
	if !tt.HasText("inside one frame") || tt.HasText("over budget") {
		t.Fatalf("7.9 ms at 120 Hz not inside one frame: %q", tt.Texts())
	}

	click(t, tt, "Copy diagnostics")
	if got := tt.Clipboard(); !strings.Contains(got, "glass to glass p50 7.90 ms p99 11.20 ms") || !strings.Contains(got, "AES-128-GCM") {
		t.Fatalf("clipboard = %q", got)
	}
	click(t, tt, "Disconnect")
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.disconnects != 1 {
		t.Fatalf("disconnects = %d", f.disconnects)
	}
}

// TestStatisticsIdle: with nothing streaming the page says so, shows the
// last session only once there was one, and the hosting bar only while
// hosting, whose Stop hosting stops it.
func TestStatisticsIdle(t *testing.T) {
	f := newFake()
	a, tt := statsTester(t, f)
	if !tt.HasText("No slices in flight") || tt.HasText("Last session") || tt.HasText("Stop hosting") {
		t.Fatalf("idle page: %q", tt.Texts())
	}
	last := liveSample()
	last.Live = false
	last.Ended = last.Started.Add(42 * time.Minute)
	f.set(func(f *fakeBackend) {
		f.last = &last
		f.hosting = HostingState{On: true, Encoder: "M5 Pro VideoToolbox", Using: HostPorts{Control: 47701, Media: 47702}}
		f.paired = []PairedClient{{Name: "iPad Pro", Fingerprint: "aa"}}
	})
	tt.Frame()
	for _, s := range []string{"42 min", "8.4 / 11.2 ms", "120.0 fps", "LAN direct", "HEVC 4:4:4 10-bit",
		"This " + machineWord(a.goos) + " is hosting", "M5 Pro VideoToolbox", "1 device"} {
		if !tt.HasText(s) {
			t.Errorf("idle page misses %q", s)
		}
	}
	snapshot(t, tt, "statistics-idle")
	click(t, tt, "Stop hosting")
	eventually(t, tt, func() bool { return !f.Hosting().On })
	click(t, tt, "Go to Hosts")
	if a.screen != ScreenHosts {
		t.Fatalf("screen %v after Go to Hosts", a.screen)
	}
}

// TestStatsHistory keeps one sample a second and starts over for a new
// stream.
func TestStatsHistory(t *testing.T) {
	var h statsHistory
	st := liveSample()
	now := time.Now()
	for i := range 5 {
		h.add(st, now.Add(time.Duration(i)*400*time.Millisecond))
	}
	if len(h.samples) != 2 {
		t.Fatalf("%d samples over 1.6 s, want 2", len(h.samples))
	}
	st.FramesLost += 3
	h.add(st, now.Add(5*time.Second))
	if got := h.samples[len(h.samples)-1].lost; got != 3 {
		t.Fatalf("lost that second = %d, want 3", got)
	}
	st.Started = st.Started.Add(time.Hour)
	h.add(st, now.Add(6*time.Second))
	if len(h.samples) != 1 || h.samples[0].lost != 0 {
		t.Fatalf("a new stream kept the old samples: %+v", h.samples)
	}
}

// TestStatisticsHistoryWhileAway: a stream's history builds while another
// page is open, and starts over for the next stream.
func TestStatisticsHistoryWhileAway(t *testing.T) {
	f := newFake()
	start := time.Now().Add(-time.Minute)
	f.set(func(f *fakeBackend) {
		f.stats = SessionStats{Live: true, Started: start, HostName: "bigboi", BitrateMbps: 42}
	})
	a, tt := newTester(t, f)
	for range 3 {
		tt.Advance(time.Second)
		tt.Frame()
	}
	if a.screen != ScreenHosts {
		t.Fatalf("screen = %v, want Hosts", a.screen)
	}
	click(t, tt, "Statistics")
	if n := len(a.stats.samples); n < 3 {
		t.Fatalf("%d samples after 3 s on Hosts, want 3 or more", n)
	}

	f.set(func(f *fakeBackend) { f.stats.Started = start.Add(time.Minute) })
	tt.Advance(time.Second)
	tt.Frame()
	if n := len(a.stats.samples); n != 1 {
		t.Fatalf("%d samples for a new stream, want 1", n)
	}
}
