package ui

import (
	"slices"
	"testing"

	"github.com/planckdotgg/planck/pkg/defaults"
	"github.com/planckdotgg/planck/pkg/discovery"
	"github.com/planckdotgg/planck/pkg/encode"
)

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

func TestSortHosts(t *testing.T) {
	hosts := []HostInfo{
		{ID: "offline", Paired: true},
		{ID: "unpaired", Online: true, RTTMs: 0.1},
		{ID: "slow", Online: true, Paired: true, RTTMs: 18},
		{ID: "unmeasured", Online: true, Paired: true},
		{ID: "fast", Online: true, Paired: true, RTTMs: 0.4},
		{ID: "active", Online: true, Paired: true, Connected: true, RTTMs: 30},
	}
	sortHosts(hosts)
	var got []string
	for _, h := range hosts {
		got = append(got, h.ID)
	}
	if want := []string{"active", "fast", "slow", "unmeasured", "unpaired", "offline"}; !slices.Equal(got, want) {
		t.Fatalf("order = %q, want %q", got, want)
	}
}

func TestHardwareLine(t *testing.T) {
	for _, c := range []struct {
		h    HostInfo
		want string
	}{
		{HostInfo{GPU: "RTX 4080", Height: 1080, RefreshHz: 60}, "RTX 4080 · 1080p 60"},
		{HostInfo{GPU: "RTX 4080", Height: 1440}, "RTX 4080 · 1440p"},
		{HostInfo{GPU: "M5 Pro"}, "M5 Pro"},
		{HostInfo{}, ""},
	} {
		if got := hardwareLine(c.h); got != c.want {
			t.Errorf("hardwareLine(%+v) = %q, want %q", c.h, got, c.want)
		}
	}
}

// TestPresetLabel: the Resolution presets show the size they resolve to
// at the client display's aspect, 16:9 when it is unknown.
func TestPresetLabel(t *testing.T) {
	for _, c := range []struct {
		w, h  int
		lines int
		want  string
	}{
		{1920, 1080, 1080, "1920 × 1080"}, {1920, 1080, 1440, "2560 × 1440"}, {1920, 1080, 2160, "3840 × 2160"},
		{0, 0, 1440, "2560 × 1440"},
		{5120, 1440, 1080, "3840 × 1080"}, {5120, 1440, 1440, "5120 × 1440"}, {5120, 1440, 2160, "7680 × 2160"},
		{3440, 1440, 1080, "2580 × 1080"}, {3440, 1440, 1440, "3440 × 1440"}, {3440, 1440, 2160, "5160 × 2160"},
	} {
		if got := presetLabel(defaults.ClientDisplay{WidthPx: c.w, HeightPx: c.h}, c.lines); got != c.want {
			t.Errorf("%dx%d display, %d lines: %q, want %q", c.w, c.h, c.lines, got, c.want)
		}
	}
}
