//go:build windows

package vt

import (
	"encoding/binary"
	"fmt"
	"testing"

	"github.com/planckdotgg/planck/pkg/capture"
	"github.com/planckdotgg/planck/pkg/encode"
	"github.com/planckdotgg/planck/pkg/encode/encodetest"
	"github.com/planckdotgg/planck/pkg/encode/nvenc"
)

// The parsers run here on real NVENC streams; the decoder itself runs on
// macOS.
func packets(t *testing.T, codec encode.Codec, chroma encode.Chroma, src capture.Format, w, h int) [][]byte {
	d := encodetest.Device(t, "NVIDIA")
	cfg := encode.Config{Codec: codec, Chroma: chroma, Source: src, Width: w, Height: h, FPS: 60, BitrateKbps: 8000}
	e, err := nvenc.Open(d.D3D, d.Info, cfg, nvenc.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	frames := encodetest.Frames(t, d, src, w, h, 3)
	var out [][]byte
	for i := 0; i < 3; i++ {
		p, err := e.Encode(frames[i], i == 0, nil)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, p.Data)
	}
	return out
}

func TestHEVCSample(t *testing.T) {
	for _, c := range []struct {
		chroma encode.Chroma
		src    capture.Format
		ten    bool
	}{{encode.NV12, capture.FormatNV12, false}, {encode.P010, capture.FormatP010, true}} {
		pk := packets(t, encode.HEVC, c.chroma, c.src, 1280, 720)
		p, sample, err := hevcSample(pk[0], nil)
		if err != nil || !p.complete() || p.tenBit() != c.ten {
			t.Fatalf("%s keyframe: params complete=%v tenBit=%v err=%v", c.chroma, p.complete(), p.tenBit(), err)
		}
		for off := 0; off < len(sample); {
			n := int(binary.BigEndian.Uint32(sample[off:]))
			if n == 0 || off+4+n > len(sample) {
				t.Fatalf("bad length prefix at %d", off)
			}
			if typ := hevcType(sample[off+4:]); typ >= nalVPS && typ <= nalPPS {
				t.Fatalf("parameter set left in the sample")
			}
			off += 4 + n
		}
		if p2, _, err := hevcSample(pk[1], nil); err != nil || p2.complete() {
			t.Fatalf("P frame: params complete=%v err=%v", p2.complete(), err)
		}
	}
}

func TestAV1Sequence(t *testing.T) {
	for _, c := range []struct {
		chroma encode.Chroma
		src    capture.Format
		high   bool
	}{{encode.NV12, capture.FormatNV12, false}, {encode.P010, capture.FormatP010, true}} {
		pk := packets(t, encode.AV1, c.chroma, c.src, 1280, 720)
		seq, sample, err := av1Sample(pk[0], nil)
		if err != nil || seq.typ != obuSequenceHeader {
			t.Fatalf("keyframe: no sequence header (%v)", err)
		}
		s, err := parseAV1Sequence(seq.payload)
		if err != nil {
			t.Fatal(err)
		}
		if s.width != 1280 || s.height != 720 || s.profile != 0 || s.highBitDepth != c.high || s.subX != 1 || s.subY != 1 || s.mono {
			t.Fatalf("parsed %+v", s)
		}
		cfg := av1C(s, seq.raw)
		if cfg[0] != 0x81 || cfg[1]>>5 != 0 || (cfg[2]>>6)&1 == 1 != c.high || string(cfg[4:]) != string(seq.raw) {
			t.Fatalf("av1C % x", cfg[:4])
		}
		obus, _ := splitOBUs(sample)
		for _, o := range obus {
			if o.typ == obuTemporalDelimiter {
				t.Fatal("temporal delimiter left in the sample")
			}
		}
		t.Logf("%s: %+v", c.chroma, s)
	}
}

func TestSplitAnnexB(t *testing.T) {
	au := []byte{0, 0, 0, 1, 0x40, 1, 2, 0, 0, 1, 0x42, 3, 0, 0, 0, 1, 0x44, 4}
	got := splitAnnexB(au)
	if fmt.Sprint(got) != "[[64 1 2] [66 3] [68 4]]" {
		t.Fatalf("%v", got)
	}
	// Two PPSs, as VideoToolbox's Main 10 output has: both must reach the
	// format description.
	au = append(au, 0, 0, 1, 0x44, 5, 0, 0, 1, 0x26, 1, 9)
	p, sample, err := hevcSample(au, nil)
	if err != nil || !p.complete() || len(p.sets) != 4 || len(sample) != 7 {
		t.Fatalf("sets %v sample % x err %v", p.sets, sample, err)
	}
	if _, err := splitOBUs([]byte{0x12, 0x05, 1}); err == nil {
		t.Fatal("truncated OBU accepted")
	}
}
