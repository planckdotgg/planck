package nvdec

import (
	"encoding/binary"
	"errors"
	"hash/crc32"
	"os"
	"testing"

	"github.com/planckdotgg/planck/pkg/capture"
	"github.com/planckdotgg/planck/pkg/decode"
	"github.com/planckdotgg/planck/pkg/encode"
)

// clip reads one of pkg/decode/vt's test clips: HEVC records carry the
// source frame's index before each access unit's length.
func clip(t *testing.T, name string, indexed bool) [][]byte {
	b, err := os.ReadFile("../vt/testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	var aus [][]byte
	for len(b) > 0 {
		if indexed {
			b = b[4:]
		}
		n := binary.BigEndian.Uint32(b)
		aus = append(aus, b[4:4+n])
		b = b[4+n:]
	}
	return aus
}

// Sum is a CRC of a picture's visible planes, which decoders of the same
// stream must agree on.
func Sum(f decode.Frame) uint32 {
	d := capture.DMABufOf(f.Surface)
	bytes := 1
	if d.Fourcc == capture.FourccP010 {
		bytes = 2
	}
	h := crc32.NewIEEE()
	for p, rows := range []int{d.Height, (d.Height + 1) / 2} {
		row := d.Width * bytes
		if p == 1 {
			row = (d.Width + 1) / 2 * 2 * bytes
		}
		base := uintptr(d.CPU[p])
		for r := range rows {
			h.Write(unsafeBytes(base+uintptr(r)*uintptr(d.Pitches[p]), row))
		}
	}
	return h.Sum32()
}

func decodeClip(t *testing.T, codec encode.Codec, aus [][]byte, w, h int, format capture.Format) []uint32 {
	if err := Available(); err != nil {
		t.Skip(err)
	}
	d, err := Open(decode.Config{Codec: codec, Width: w, Height: h})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var sums []uint32
	for i, au := range aus {
		f, err := d.Decode(au, int64(i))
		if err != nil {
			t.Fatalf("access unit %d: %v", i, err)
		}
		if f.Width != w || f.Height != h || f.Format != format || f.PTS != int64(i) {
			t.Fatalf("access unit %d: %dx%d %v pts %d", i, f.Width, f.Height, f.Format, f.PTS)
		}
		sums = append(sums, Sum(f))
		if i == 0 {
			t.Logf("first picture in %v", f.Latency)
		}
		d.Release(f)
	}
	distinct := map[uint32]bool{}
	for _, s := range sums {
		distinct[s] = true
	}
	if len(distinct) < len(sums)/2 {
		t.Errorf("%d distinct pictures of %d", len(distinct), len(sums))
	}
	return sums
}

// TestDecodeHEVC plays NVENC's HEVC clip with frames 20 to 22 lost and
// their references invalidated.
func TestDecodeHEVC(t *testing.T) {
	sums := decodeClip(t, encode.HEVC, clip(t, "hevc_refinv_640x360.bin", true), 640, 360, capture.FormatNV12)
	t.Logf("%d pictures, CRCs %08x ... %08x", len(sums), sums[0], sums[len(sums)-1])
}

// TestDecodeAV1 plays NVENC's 10-bit AV1 clip.
func TestDecodeAV1(t *testing.T) {
	sums := decodeClip(t, encode.AV1, clip(t, "av1_640x360.tus", false), 640, 360, capture.FormatP010)
	t.Logf("%d pictures, CRCs %08x ... %08x", len(sums), sums[0], sums[len(sums)-1])
}

func TestOpenRejectsOtherCodecs(t *testing.T) {
	if _, err := Open(decode.Config{Codec: encode.PyroWave}); !errors.Is(err, decode.ErrUnsupported) {
		t.Fatalf("PyroWave: %v", err)
	}
}

func TestPoolRunsOutWithoutRelease(t *testing.T) {
	if err := Available(); err != nil {
		t.Skip(err)
	}
	d, err := Open(decode.Config{Codec: encode.HEVC})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var kept []decode.Frame
	var last error
	for i, au := range clip(t, "hevc_refinv_640x360.bin", true) {
		f, err := d.Decode(au, int64(i))
		if err != nil {
			last = err
			break
		}
		kept = append(kept, f)
	}
	if last == nil || len(kept) != 8 {
		t.Fatalf("kept %d pictures, then %v", len(kept), last)
	}
	for _, f := range kept {
		d.Release(f)
	}
}
