//go:build darwin

package vt

import (
	"encoding/binary"
	"errors"
	"math"
	"os"
	"testing"
	"unsafe"

	"github.com/watzon/planck/internal/apple"
	"github.com/watzon/planck/internal/bench"
	"github.com/watzon/planck/internal/synth"
	"github.com/watzon/planck/pkg/capture"
	"github.com/watzon/planck/pkg/decode"
	"github.com/watzon/planck/pkg/encode"
	encvt "github.com/watzon/planck/pkg/encode/vt"
)

var (
	getFormat          func(sbuf uintptr) uintptr
	getParameterSet    func(f uintptr, i uint, set *uintptr, size *uint, count *uint, nalLen *int32) int32
	cvLock             func(pb uintptr, flags uint64) int32
	cvUnlock           func(pb uintptr, flags uint64) int32
	cvPlaneBase        func(pb uintptr, plane uint) uintptr
	cvPlaneBytesPerRow func(pb uintptr, plane uint) uint
)

func init() {
	apple.Register(&getFormat, apple.CoreMedia, "CMSampleBufferGetFormatDescription")
	apple.Register(&getParameterSet, apple.CoreMedia, "CMVideoFormatDescriptionGetHEVCParameterSetAtIndex")
	apple.Register(&cvLock, apple.CoreVideo, "CVPixelBufferLockBaseAddress")
	apple.Register(&cvUnlock, apple.CoreVideo, "CVPixelBufferUnlockBaseAddress")
	apple.Register(&cvPlaneBase, apple.CoreVideo, "CVPixelBufferGetBaseAddressOfPlane")
	apple.Register(&cvPlaneBytesPerRow, apple.CoreVideo, "CVPixelBufferGetBytesPerRowOfPlane")
}

const w, h = 1920, 1080

// hevcStream encodes n synthetic frames with VideoToolbox.
func hevcStream(t *testing.T, tenBit bool, n int) [][]byte {
	t.Helper()
	chroma, src, pixel := encode.NV12, capture.FormatNV12, uint32(encvt.Pixel420v)
	planes := func(i int) ([][]byte, []int) {
		b := make([]byte, w*h*3/2)
		synth.NV12(b, w, h, i)
		return [][]byte{b[:w*h], b[w*h:]}, []int{w, w}
	}
	if tenBit {
		chroma, src, pixel = encode.P010, capture.FormatP010, encvt.PixelX420
		planes = func(i int) ([][]byte, []int) {
			b := make([]byte, w*h*3)
			synth.P010(b, w, h, i)
			return [][]byte{b[:w*h*2], b[w*h*2:]}, []int{w * 2, w * 2}
		}
	}
	enc, err := encvt.Open(encode.Config{Codec: encode.HEVC, Chroma: chroma, Source: src, Width: w, Height: h, FPS: 60, BitrateKbps: 80000})
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	enc.Keep = true
	var out [][]byte
	for i := 0; i < n; i++ {
		pb, err := encvt.NewPixelBuffer(w, h, pixel)
		if err != nil {
			t.Fatal(err)
		}
		p, rows := planes(i)
		encvt.FillPixelBuffer(pb, p, rows)
		pkt, err := enc.Encode(capture.Frame{Surface: pb, Format: src, Width: w, Height: h}, i == 0, nil)
		apple.CFRelease(pb)
		if err != nil {
			t.Fatal(err)
		}
		var au []byte
		if i == 0 {
			au = parameterSets(t, enc.LastSample)
		}
		apple.CFRelease(enc.LastSample)
		out = append(out, append(au, toAnnexB(pkt.Data)...))
	}
	return out
}

// parameterSets returns the VPS, SPS and PPS VideoToolbox keeps in the
// sample's format description, in Annex B, as a host puts them before a
// keyframe.
func parameterSets(t *testing.T, sbuf uintptr) []byte {
	f := getFormat(sbuf)
	var out []byte
	for i := uint(0); i < 3; i++ {
		var p uintptr
		var size, count uint
		var nalLen int32
		if st := getParameterSet(f, i, &p, &size, &count, &nalLen); st != 0 {
			t.Fatalf("parameter set %d: %d", i, st)
		}
		out = append(out, 0, 0, 0, 1)
		out = append(out, unsafe.Slice((*byte)(cMemory(p)), size)...)
	}
	return out
}

// toAnnexB turns VideoToolbox's length-prefixed output into Annex B.
func toAnnexB(b []byte) []byte {
	var out []byte
	for len(b) >= 4 {
		n := int(binary.BigEndian.Uint32(b))
		out = append(out, 0, 0, 0, 1)
		out = append(out, b[4:4+n]...)
		b = b[4+n:]
	}
	return out
}

func lumaPSNR(t *testing.T, f decode.Frame, i int) (float64, float64) {
	pb := f.Ref
	cvLock(pb, 1)
	defer cvUnlock(pb, 1)
	base, stride := cvPlaneBase(pb, 0), int(cvPlaneBytesPerRow(pb, 0))
	psnr := func(frame int) float64 {
		ref := make([]byte, w*h*3/2)
		synth.NV12(ref, w, h, frame)
		var se float64
		for y := 0; y < h; y++ {
			row := unsafe.Slice((*byte)(cMemory(base+uintptr(y*stride))), stride)
			for x := 0; x < w; x++ {
				got := float64(row[x])
				if f.Format == capture.FormatP010 {
					got = float64(uint16(row[2*x])|uint16(row[2*x+1])<<8) / 256
				}
				d := got - float64(ref[y*w+x])
				se += d * d
			}
		}
		return 10 * math.Log10(255*255/math.Max(se/float64(w*h), 1e-9))
	}
	return psnr(i), psnr(i + 1)
}

func TestDecodeHEVC(t *testing.T) {
	for _, ten := range []bool{false, true} {
		packets := hevcStream(t, ten, 60)
		dec, err := Open(decode.Config{Codec: encode.HEVC, Width: w, Height: h, FPS: 60})
		if err != nil {
			t.Fatal(err)
		}
		want := capture.FormatNV12
		if ten {
			want = capture.FormatP010
		}
		lat := bench.NewSamples(len(packets))
		for i, p := range packets {
			f, err := dec.Decode(p, int64(i))
			if err != nil {
				t.Fatalf("10-bit=%v frame %d: %v", ten, i, err)
			}
			if f.Format != want || f.Width != w || f.Height != h || f.Surface == 0 || f.PTS != int64(i) {
				t.Fatalf("frame %d: %s %dx%d surface=%#x pts=%d", i, f.Format, f.Width, f.Height, f.Surface, f.PTS)
			}
			if i > 0 && i%20 == 0 {
				good, next := lumaPSNR(t, f, i)
				t.Logf("10-bit=%v frame %d: luma PSNR %.1f dB (next source frame %.1f dB)", ten, i, good, next)
				if good < 35 || good < next+10 {
					t.Fatalf("the picture is not the source")
				}
			}
			lat.Add(float64(f.Latency) / 1e6)
			dec.Release(f)
		}
		t.Logf("10-bit=%v: decode %s", ten, lat.Summarize())
		dec.Close()
	}
}

func TestDecodeAV1(t *testing.T) {
	clip, err := os.ReadFile("testdata/av1_640x360.tus")
	if err != nil {
		t.Fatal(err)
	}
	dec, err := Open(decode.Config{Codec: encode.AV1, FPS: 60})
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()
	for i := 0; len(clip) > 4; i++ {
		n := int(binary.BigEndian.Uint32(clip))
		f, err := dec.Decode(clip[4:4+n], int64(i))
		clip = clip[4+n:]
		if errors.Is(err, decode.ErrUnsupported) {
			t.Skip(err)
		}
		if err != nil {
			t.Fatalf("temporal unit %d: %v", i, err)
		}
		if f.Format != capture.FormatP010 || f.Width != 640 || f.Height != 360 {
			t.Fatalf("temporal unit %d: %s %dx%d", i, f.Format, f.Width, f.Height)
		}
		dec.Release(f)
	}
}
