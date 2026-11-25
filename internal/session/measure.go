package session

import (
	"context"
	"errors"
	"time"

	"github.com/planckdotgg/planck/pkg/capture"
	"github.com/planckdotgg/planck/pkg/encode"
	"github.com/planckdotgg/planck/pkg/transport"
)

// measureWarmup frames are encoded before MeasureEncoder counts: the
// first ones pay for the encoder's start and the GPU's clock ramp.
const measureWarmup = 15

// MeasureEncoder encodes frames synthetic 1080p120 HEVC pictures on the
// GPU adapter names (a part of its name, as HostConfig.Adapter) and
// returns the encode time, from each picture's present to its packet.
// open nil is DefaultVideoOpener.
func MeasureEncoder(ctx context.Context, open VideoOpener, adapter string, frames int) (Latency, error) {
	if open == nil {
		open = DefaultVideoOpener
	}
	pipe, err := open(VideoParams{Adapter: adapter, Synth: true, Codec: encode.HEVC, Chroma: encode.NV12,
		Width: 1920, Height: 1080, FPS: 120, BitrateKbps: 50_000, Slices: 1})
	if err != nil {
		return Latency{}, err
	}
	defer pipe.Close()
	w := newWindow(frames)
	buf := make([]byte, 0, 2<<20)
	for n := 0; w.len() < frames; n++ {
		if err := ctx.Err(); err != nil {
			return Latency{}, err
		}
		_, captureTS, err := pipe.Next(time.Second, n == 0, buf)
		switch {
		case errors.Is(err, capture.ErrTimeout) || errors.Is(err, encode.ErrDropped):
			continue
		case err != nil:
			return Latency{}, err
		}
		if n >= measureWarmup {
			w.add(ms(transport.Mono() - captureTS))
		}
	}
	return w.summary(), nil
}
