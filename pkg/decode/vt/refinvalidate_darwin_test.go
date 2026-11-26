package vt

import (
	"encoding/binary"
	"math"
	"os"
	"testing"
	"unsafe"

	"github.com/planckdotgg/planck/internal/synth"
	"github.com/planckdotgg/planck/pkg/decode"
	"github.com/planckdotgg/planck/pkg/encode"
)

// TestDecodeAfterReferenceInvalidation plays NVENC's stream of a lost
// frame recovered without a keyframe (TestWriteRefInvalidationClip):
// frames 20 to 22 never reach the decoder, and frame 23, encoded after
// the host invalidated them, predicts only from frames VideoToolbox has.
// Every picture must decode and match its source as well as the pictures
// before the loss did.
func TestDecodeAfterReferenceInvalidation(t *testing.T) {
	const cw, ch = 640, 360
	clip, err := os.ReadFile("testdata/hevc_refinv_640x360.bin")
	if err != nil {
		t.Fatal(err)
	}
	ref := make([]byte, cw*ch*3/2)
	psnr := func(f decode.Frame, frame int) float64 {
		pb := f.Ref
		cvLock(pb, 1)
		defer cvUnlock(pb, 1)
		base, stride := cvPlaneBase(pb, 0), int(cvPlaneBytesPerRow(pb, 0))
		synth.NV12(ref, cw, ch, frame)
		var se float64
		for y := 0; y < ch; y++ {
			row := unsafe.Slice((*byte)(cMemory(base+uintptr(y*stride))), stride)
			for x := 0; x < cw; x++ {
				d := float64(row[x]) - float64(ref[y*cw+x])
				se += d * d
			}
		}
		return 10 * math.Log10(255*255/math.Max(se/float64(cw*ch), 1e-9))
	}
	// play decodes the clip, leaving out skip too, and returns each
	// picture's luma PSNR against its source and against the next frame,
	// by source index; a picture that failed to decode has none.
	play := func(skip int) (got, next map[int]float64) {
		dec, err := Open(decode.Config{Codec: encode.HEVC, Width: cw, Height: ch, FPS: 60})
		if err != nil {
			t.Fatal(err)
		}
		defer dec.Close()
		got, next = map[int]float64{}, map[int]float64{}
		for b := clip; len(b) > 8; {
			i := int(binary.BigEndian.Uint32(b))
			n := int(binary.BigEndian.Uint32(b[4:]))
			au := b[8 : 8+n]
			b = b[8+n:]
			if i == skip {
				continue
			}
			f, err := dec.Decode(au, int64(i))
			if err != nil {
				continue
			}
			got[i], next[i] = psnr(f, i), psnr(f, i+1)
			dec.Release(f)
		}
		return got, next
	}
	// The clip is 1.5 Mbit/s: quality climbs to about 26 dB by frame 19.
	// A picture after the loss must be as good as the last one before it,
	// and far from the next frame's source.
	got, next := play(-1)
	before := got[19]
	worst := 1000.0
	for i := 23; i < 48; i++ {
		p, ok := got[i]
		if !ok {
			t.Fatalf("frame %d did not decode", i)
		}
		if p < before-3 || p < next[i]+8 {
			t.Fatalf("frame %d: luma PSNR %.1f dB against its source (%.1f before the loss), %.1f against the next", i, p, before, next[i])
		}
		worst = min(worst, p)
	}
	// Control: leave out frame 23 as well. Frame 24 then predicts from a
	// picture VideoToolbox never had.
	ctl, ctlNext := play(23)
	p24, ok := ctl[24]
	t.Logf("frame 19 (before the loss) %.1f dB; frames 23-47 after it: worst %.1f dB; control without frame 23: frame 24 decoded %v at %.1f dB (next %.1f)",
		before, worst, ok, p24, ctlNext[24])
	if ok && p24 >= before-3 && p24 >= ctlNext[24]+8 {
		t.Fatalf("frame 24 without its reference still matches its source (%.1f dB): the test cannot see a bad reference", p24)
	}
}
