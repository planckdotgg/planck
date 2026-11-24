package session

import (
	"testing"
	"time"

	"github.com/planckdotgg/planck/pkg/decode"
)

// deferredDecoder returns pictures before they are written, as the
// PyroWave GPU decoders do.
type deferredDecoder struct{ finished []int64 }

func (d *deferredDecoder) Decode(_ []byte, pts int64) (decode.Frame, error) {
	return decode.Frame{Surface: 1, PTS: pts}, nil
}
func (d *deferredDecoder) Finish(f *decode.Frame) {
	d.finished = append(d.finished, f.PTS)
	f.Latency = 3 * time.Millisecond
}
func (d *deferredDecoder) Release(decode.Frame) {}
func (d *deferredDecoder) Close() error         { return nil }

func TestDecodeSinkWaitsForDeferredDecoders(t *testing.T) {
	dec := &deferredDecoder{}
	s := &decodeSink{dec: dec}
	got, err := s.Submit([]byte{1}, 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(dec.finished) != 1 || dec.finished[0] != 7 || got.Decode != 3*time.Millisecond {
		t.Fatalf("finished %v, decode time %v; want frame 7 finished before Submit returns, 3ms", dec.finished, got.Decode)
	}
}
