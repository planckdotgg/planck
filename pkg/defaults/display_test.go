package defaults

import (
	"strings"
	"testing"
)

// twoDisplays is bigboi with a second, HDR display on the RTX 4080.
func twoDisplays() HostCapabilities {
	h := bigboi()
	h.Displays = append(h.Displays, HostDisplay{ID: `\\.\DISPLAY2`, Name: "LG OLED", Width: 3840, Height: 2160, RefreshHz: 120, Adapter: rtx4080.LUID, HDR: true})
	return h
}

func TestResolvePickedDisplay(t *testing.T) {
	p := Resolve(twoDisplays(), m5Pro(), lan, UserOverrides{Display: `\\.\DISPLAY2`})
	if p.VirtualDisplay || p.CaptureDisplay != `\\.\DISPLAY2` {
		t.Fatalf("virtual %v, capture %q: a picked display streams that display", p.VirtualDisplay, p.CaptureDisplay)
	}
	if p.Width != 3840 || p.Height != 2160 || p.EncodeAdapter.LUID != rtx4080.LUID {
		t.Errorf("%dx%d on %s, want the display's 3840x2160 on its own GPU", p.Width, p.Height, p.EncodeAdapter.Name)
	}
	if d := decision(p, "display"); d.Value != `\\.\DISPLAY2` || d.Source != SourceUser {
		t.Errorf("display decision %+v", d)
	}
	if d := decision(p, "virtualDisplay"); !strings.Contains(d.Why, "picked display") {
		t.Errorf("virtualDisplay why %q", d.Why)
	}
}

func TestResolvePickedDisplayFallbacks(t *testing.T) {
	p := Resolve(twoDisplays(), m5Pro(), lan, UserOverrides{Display: `\\.\DISPLAY9`, VirtualDisplay: ToggleOff})
	if p.CaptureDisplay != `\\.\DISPLAY1` {
		t.Errorf("unknown display: capture %q, want the primary", p.CaptureDisplay)
	}
	if d := decision(p, "display"); d.Source != SourceFallback || d.Why != "the host has no such display" {
		t.Errorf("unknown display decision %+v", d)
	}
	if !p.VirtualDisplay && len(p.Warnings) == 0 {
		t.Error("an unknown display warns")
	}

	h := twoDisplays()
	h.PinnedDisplay = `\\.\DISPLAY1`
	p = Resolve(h, m5Pro(), lan, UserOverrides{Display: `\\.\DISPLAY2`})
	if p.CaptureDisplay != `\\.\DISPLAY1` {
		t.Errorf("pinned host: capture %q", p.CaptureDisplay)
	}
	if d := decision(p, "display"); d.Source != SourceFallback || !strings.Contains(d.Why, "pins") {
		t.Errorf("pinned host decision %+v", d)
	}
}

func TestResolveHDR10(t *testing.T) {
	mac := m5Pro()
	mac.PresentHDR10 = true
	pick := UserOverrides{Display: `\\.\DISPLAY2`}
	p := Resolve(twoDisplays(), mac, lan, pick)
	if !p.HDR10 || p.BitDepth != 10 {
		t.Fatalf("HDR desktop, HDR client: hdr10 %v depth %d", p.HDR10, p.BitDepth)
	}
	if d := decision(p, "hdr10"); d.Value != "on" {
		t.Errorf("hdr10 decision %+v", d)
	}

	sdrClient := mac
	sdrClient.Display.HDR = false
	noPresent := m5Pro()
	cases := []struct {
		name   string
		host   HostCapabilities
		client ClientCapabilities
		o      UserOverrides
		why    string
	}{
		{"off", twoDisplays(), mac, UserOverrides{Display: pick.Display, HDR10: ToggleOff}, "override"},
		{"SDR client display", twoDisplays(), sdrClient, pick, "the client display is not in HDR"},
		{"client cannot present", twoDisplays(), noPresent, pick, "the client cannot present HDR10"},
		{"SDR host desktop", twoDisplays(), mac, UserOverrides{Display: `\\.\DISPLAY1`}, "the host desktop is not in HDR"},
		{"virtual display", bigboi(), mac, UserOverrides{}, "the virtual display is SDR"},
		{"H.264", twoDisplays(), mac, UserOverrides{Display: pick.Display, Codec: "h264"}, "HDR10 needs HEVC or AV1, not h264"},
		{"8-bit asked", twoDisplays(), mac, UserOverrides{Display: pick.Display, BitDepth: "8"}, "bit depth 8 is set"},
	}
	for _, c := range cases {
		p := Resolve(c.host, c.client, lan, c.o)
		if p.HDR10 {
			t.Errorf("%s: HDR10 on", c.name)
		}
		if d := decision(p, "hdr10"); d.Value != "off" || d.Why != c.why {
			t.Errorf("%s: hdr10 decision %+v, want why %q", c.name, d, c.why)
		}
	}
}

func TestValidateDisplayAndHDR(t *testing.T) {
	if err := (UserOverrides{Display: `\\.\DISPLAY2`, HDR10: ToggleOff}).Validate(); err != nil {
		t.Error(err)
	}
	for _, o := range []UserOverrides{{HDR10: ToggleOn}, {Display: strings.Repeat("x", MaxDisplayIDLen+1)}} {
		if o.Validate() == nil {
			t.Errorf("%+v accepted", o)
		}
	}
	if m := Merge(UserOverrides{Display: "1", HDR10: ToggleOff}, UserOverrides{Display: "2"}); m.Display != "2" || m.HDR10 != ToggleOff {
		t.Errorf("Merge %+v", m)
	}
}
