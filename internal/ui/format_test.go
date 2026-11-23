package ui

import (
	"slices"
	"testing"

	"github.com/planckdotgg/planck/pkg/discovery"
	"github.com/planckdotgg/planck/pkg/encode"
)

func TestHUDSummary(t *testing.T) {
	tel := sampleTelemetry()
	if got, want := hudSummary(tel), "8.4 ms · 240 FPS · 80 Mbps · LAN direct · HEVC 10-bit"; got != want {
		t.Fatalf("hudSummary = %q, want %q", got, want)
	}
	tel = Telemetry{TotalMs: 23.04, FPS: 59.6, BitrateMbps: 7.25, PathKind: discovery.PathTailscaleDERP,
		Codec: encode.AV1, Chroma: encode.YUV444P10}
	if got, want := hudSummary(tel), "23.0 ms · 60 FPS · 7.2 Mbps · DERP relay · AV1 4:4:4 10-bit"; got != want {
		t.Fatalf("hudSummary = %q, want %q", got, want)
	}
	if got, want := hudSummary(Telemetry{}), "0.0 ms · 0 FPS · 0.0 Mbps · LAN direct"; got != want {
		t.Fatalf("hudSummary without codec = %q, want %q", got, want)
	}
}

func TestCodecLabel(t *testing.T) {
	cases := []struct {
		c    encode.Codec
		ch   encode.Chroma
		want string
	}{
		{encode.H264, encode.NV12, "H.264"},
		{encode.HEVC, encode.P010, "HEVC 10-bit"},
		{encode.HEVC, encode.YUV444, "HEVC 4:4:4 8-bit"},
		{encode.AV1, encode.YUV444P10, "AV1 4:4:4 10-bit"},
		{0, encode.NV12, ""},
	}
	for _, c := range cases {
		if got := codecLabel(c.c, c.ch); got != c.want {
			t.Errorf("codecLabel(%v, %v) = %q, want %q", c.c, c.ch, got, c.want)
		}
	}
}

func TestPathBars(t *testing.T) {
	got := []int{pathBars(discovery.PathLANDirect), pathBars(discovery.PathTailscaleDirect), pathBars(discovery.PathTailscaleDERP)}
	if !slices.Equal(got, []int{3, 2, 1}) {
		t.Fatalf("bars = %v, want [3 2 1]", got)
	}
}

func TestCleanPIN(t *testing.T) {
	for in, want := range map[string]string{"481207": "481207", "48 12-07": "481207", "4812079": "481207", "a1b2": "12", "": ""} {
		if got := cleanPIN(in); got != want {
			t.Errorf("cleanPIN(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFingerprintLines(t *testing.T) {
	fp := "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
	want := []string{"9F86 D081 884C 7D65 9A2F EAA0 C55A D015", "A3BF 4F1B 2B0B 822C D15D 6C15 B0F0 0A08"}
	if got := fingerprintLines(fp); !slices.Equal(got, want) {
		t.Fatalf("fingerprintLines = %q", got)
	}
	if got := fingerprintLines("9F:86:D0"); !slices.Equal(got, []string{"9F86 D0"}) {
		t.Fatalf("colon form = %q", got)
	}
	if got := fingerprintLines(""); len(got) != 0 {
		t.Fatalf("empty = %q", got)
	}
}

func TestFeaturedHost(t *testing.T) {
	hosts := sampleHosts()
	hosts[0].Online = false
	if got := featuredHost(hosts); got != 1 {
		t.Fatalf("featured = %d, want the first online (1)", got)
	}
	for i := range hosts {
		hosts[i].Online = false
	}
	if got := featuredHost(hosts); got != 0 {
		t.Fatalf("featured with none online = %d, want 0", got)
	}
	if got := featuredHost(nil); got != -1 {
		t.Fatalf("featured of none = %d", got)
	}
}
