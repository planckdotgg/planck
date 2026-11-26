//go:build linux && !android && (amd64 || arm64)

package vaapi

import (
	"bytes"
	"runtime"
	"slices"
	"testing"
	"unsafe"

	"github.com/planckdotgg/planck/internal/synth"
	"github.com/planckdotgg/planck/pkg/capture"
	"github.com/planckdotgg/planck/pkg/codec/hevc"
	"github.com/planckdotgg/planck/pkg/encode"
)

// TestStructLayouts holds the Go structs passed to libva to the sizes gcc
// gives libva 2.20's on linux/amd64.
func TestStructLayouts(t *testing.T) {
	for _, c := range []struct {
		name      string
		got, want uintptr
	}{
		{"VADRMPRIMESurfaceDescriptor", unsafe.Sizeof(drmPrimeDescriptor{}), 312},
		{"VAImage", unsafe.Sizeof(vaImage{}), 120},
		{"VASurfaceAttrib", unsafe.Sizeof(surfaceAttrib{}), 24},
		{"VAConfigAttrib", unsafe.Sizeof(configAttrib{}), 8},
		{"VARectangle", unsafe.Sizeof(rect{}), 8},
	} {
		if c.got != c.want {
			t.Errorf("%s: %d bytes, libva's are %d", c.name, c.got, c.want)
		}
	}
}

func TestLevel(t *testing.T) {
	for _, c := range []struct {
		w, h, fps int
		want      uint32
	}{
		{1280, 720, 60, 120}, {1920, 1080, 60, 123}, {1920, 1080, 120, 150}, {1920, 1080, 240, 153},
		{3840, 2160, 60, 153}, {3840, 2160, 120, 156}, {7680, 4320, 60, 183},
	} {
		if got := level(c.w, c.h, c.fps); got != c.want {
			t.Errorf("%dx%d@%d: level %d, want %d", c.w, c.h, c.fps, got, c.want)
		}
	}
}

// TestStreamFollowsTheDriver: block sizes set the CTB, coding and
// transform sizes, and a feature the driver requires is turned on.
func TestStreamFollowsTheDriver(t *testing.T) {
	cfg := encode.Config{Width: 1920, Height: 1080, FPS: 60}
	s := streamFor(cfg, 0, vaAttribNotSupported, vaAttribNotSupported)
	if s.Log2MinCbMinus3 != 1 || s.Log2DiffMaxMinCb != 1 || s.Height != 1088 || s.VisibleHeight != 1080 || !s.CuQPDelta || s.SAO {
		t.Fatalf("defaults: %+v", s)
	}
	// 64x64 CTBs, 8x8 coding blocks, 4x4 to 32x32 transforms, depth 2;
	// SAO required, AMP supported but optional, cu_qp_delta unsupported.
	blocks := uint32(3 | 0<<4 | 3<<6 | 0<<8 | 2<<10 | 2<<14)
	features := uint32(1<<featAMP | featRequired<<featSAO)
	s = streamFor(cfg, 2, features, blocks)
	if s.Log2MinCbMinus3 != 0 || s.Log2DiffMaxMinCb != 3 || s.Log2DiffMaxMinTb != 3 || s.MaxTHDepthInter != 2 || s.Height != 1080 {
		t.Fatalf("block sizes: %+v", s)
	}
	if !s.SAO || s.AMP || s.CuQPDelta || s.ProfileIdc != 2 || s.BitDepthMinus8 != 2 {
		t.Fatalf("features: %+v", s)
	}
}

func TestKeyframesCarryParameterSets(t *testing.T) {
	st := streamFor(encode.Config{Width: 640, Height: 360, FPS: 60}, 0, vaAttribNotSupported, vaAttribNotSupported)
	ps := slices.Concat([]byte{0, 0, 0, 1}, st.VPS(), []byte{0, 0, 0, 1}, st.SPS(), []byte{0, 0, 0, 1}, st.PPS())
	idr := slices.Concat([]byte{0, 0, 0, 1}, st.SliceHeader(hevc.Slice{IDR: true, Type: hevc.SliceI}))
	e := &Encoder{}
	if got := e.withParameterSets(slices.Concat(ps, idr)); !bytes.Equal(got, slices.Concat(ps, idr)) {
		t.Fatal("a keyframe with parameter sets changed")
	}
	if got := e.withParameterSets(bytes.Clone(idr)); !bytes.Equal(got, slices.Concat(ps, idr)) {
		t.Fatalf("a bare keyframe did not get the parameter sets: %x", got)
	}
}

// TestLiveEncode encodes BGRX and NV12 frames from memory on the first
// VA-API driver that encodes HEVC, and checks each keyframe starts with
// parameter sets the HEVC parser reads at the right size.
func TestLiveEncode(t *testing.T) {
	all, err := Adapters()
	if err != nil {
		t.Skip(err)
	}
	const w, h = 1280, 720
	for _, src := range []capture.Format{capture.FormatBGRA8, capture.FormatNV12} {
		t.Run(src.String(), func(t *testing.T) {
			cfg := encode.Config{Codec: encode.HEVC, Chroma: encode.NV12, Source: src, Width: w, Height: h, FPS: 60, BitrateKbps: 10000, Slices: 2}
			e, err := Open(all[0], cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			t.Logf("%s: packed headers %v, low-delay B %v, %d slices", e.adapter.Name, e.packed, e.lowDelayB, len(e.slices)-1)
			var keep [][]byte
			for i := range 8 {
				var d *capture.DMABuf
				if src == capture.FormatNV12 {
					b := make([]byte, w*h*3/2)
					synth.NV12(b, w, h, i*8)
					d = capture.MemoryDMABuf(capture.FourccNV12, w, h, [][]byte{b[:w*h], b[w*h:]}, []int{w, w})
					keep = append(keep, b)
				} else {
					b := make([]byte, w*h*4)
					synth.BGRA(b, w, h, i*8)
					d = capture.MemoryDMABuf(capture.FourccXRGB8888, w, h, [][]byte{b}, []int{w * 4})
					keep = append(keep, b)
				}
				pkt, err := e.Encode(capture.Frame{Surface: d.Handle(), Format: src, Width: w, Height: h}, i == 0 || i == 5, nil)
				runtime.KeepAlive(d)
				if err != nil {
					t.Fatalf("frame %d: %v", i, err)
				}
				if pkt.Keyframe != (i == 0 || i == 5) || len(pkt.Data) == 0 {
					t.Fatalf("frame %d: keyframe %v, %d bytes", i, pkt.Keyframe, len(pkt.Data))
				}
				if pkt.Keyframe {
					var sps *hevc.SPS
					for _, n := range hevc.NALUnits(pkt.Data) {
						if hevc.NALType(n) == hevc.NalSPS {
							sps, _ = hevc.ParseSPS(n)
						}
					}
					if sps == nil {
						t.Fatalf("frame %d: keyframe without an SPS", i)
					}
					if vw, vh := sps.VisibleSize(); vw != w || vh != h {
						t.Fatalf("frame %d: SPS shows %dx%d", i, vw, vh)
					}
				}
			}
			runtime.KeepAlive(keep)
		})
	}
}
