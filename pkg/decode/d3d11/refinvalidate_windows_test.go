package d3d11

import (
	"errors"
	"fmt"
	"testing"

	"github.com/planckdotgg/planck/pkg/capture"
	"github.com/planckdotgg/planck/pkg/decode"
	"github.com/planckdotgg/planck/pkg/encode"
	"github.com/planckdotgg/planck/pkg/encode/encodetest"
	"github.com/planckdotgg/planck/pkg/encode/nvenc"
)

// TestReferenceInvalidationDecodes is the host and client of a lost frame
// end to end, on the RTX 4080's NVENC and the Media Foundation HEVC
// decoder: frames 40 and 41 are lost, 42 predicts from them and is
// skipped, the host invalidates 40 to 42, and the decoder goes on from 43
// with no keyframe. Each picture from 43 on must match its source. The
// control run skips the invalidation: 43 then predicts from a picture the
// decoder never had, and the decoder gives no picture that matches its
// source (Media Foundation gives none at all until a keyframe).
func TestReferenceInvalidationDecodes(t *testing.T) {
	const n, lostFrom, lostTo = 120, 40, 42
	for _, c := range []struct {
		chroma encode.Chroma
		src    capture.Format
	}{
		{encode.NV12, capture.FormatNV12},
		{encode.P010, capture.FormatP010},
	} {
		for _, invalidate := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s_invalidate_%v", c.chroma, invalidate), func(t *testing.T) {
				nv := encodetest.Device(t, "NVIDIA")
				cfg := encode.Config{Codec: encode.HEVC, Chroma: c.chroma, Source: c.src, Width: w, Height: h, FPS: 120, BitrateKbps: 150000}
				enc, err := nvenc.Open(nv.D3D, nv.Info, cfg, nvenc.Options{})
				if err != nil {
					t.Fatal(err)
				}
				defer enc.Close()
				if !enc.Caps().RefInvalidation {
					t.Skip("NVENC without reference invalidation")
				}
				frames := encodetest.Frames(t, nv, c.src, w, h, n)
				dec, err := Open(encodetest.GPU(nv), decode.Config{Codec: encode.HEVC, Width: w, Height: h, FPS: 120})
				if errors.Is(err, decode.ErrUnsupported) {
					t.Skip(err)
				}
				if err != nil {
					t.Fatal(err)
				}
				defer dec.Close()
				var worst, worstControl, first float64 = 1000, 0, 0
				keys, after := 0, 0
				for i := 0; i < n; i++ {
					if invalidate && i == lostTo+1 {
						for lost := lostFrom; lost <= lostTo; lost++ {
							if err := enc.InvalidateRefs(int64(lost)); err != nil {
								t.Fatal(err)
							}
						}
					}
					pkt, err := enc.Encode(frames[i], i == 0, nil)
					if err != nil {
						t.Fatal(err)
					}
					if pkt.Keyframe {
						keys++
					}
					if i >= lostFrom && i <= lostTo {
						continue
					}
					f, err := dec.Decode(pkt.Data, int64(i))
					if errors.Is(err, decode.ErrNoFrame) {
						continue
					}
					if err != nil && !invalidate && i > lostTo {
						continue
					}
					if err != nil {
						t.Fatalf("frame %d: %v", i, err)
					}
					if i > lostTo {
						after++
					}
					if i > lostTo && (i%4 == 3 || after == 1) {
						p, ctl := lumaPSNR(t, nv, f, i)
						if after == 1 {
							first = p
						}
						worst, worstControl = min(worst, p), max(worstControl, ctl)
					}
					dec.Release(f)
				}
				t.Logf("invalidate %v: %d keyframe(s); %d of %d pictures after the loss; luma PSNR %.1f dB first, %.1f dB worst (next source frame: %.1f dB best)",
					invalidate, keys, after, n-lostTo-1, first, worst, worstControl)
				if keys != 1 {
					t.Fatalf("%d keyframes, want only the first", keys)
				}
				if invalidate && (after != n-lostTo-1 || worst < 35 || worst < worstControl+10) {
					t.Fatalf("with the lost frames invalidated: %d of %d pictures, worst %.1f dB from its source", after, n-lostTo-1, worst)
				}
				if !invalidate && after > 0 && first >= 35 {
					t.Fatalf("without invalidation the first picture after the loss matches its source (%.1f dB): the test cannot see a bad reference", first)
				}
			})
		}
	}
}
