// Package decode defines the hardware video decoder contract shared by the
// platform backends: Media Foundation on Direct3D 11 (Windows) and
// VideoToolbox (macOS). Decoded pictures stay on the GPU.
package decode

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/planckdotgg/planck/pkg/capture"
	"github.com/planckdotgg/planck/pkg/encode"
	"github.com/planckdotgg/planck/pkg/gpu"
)

// Frame is a decoded picture owned by the decoder until Release.
type Frame struct {
	// Surface is the picture: an ID3D11Texture2D* on Windows, an
	// IOSurfaceRef on macOS.
	Surface uintptr
	// Slice is the array slice of Surface that holds the picture; Windows
	// decoders write into texture arrays. 0 on macOS.
	Slice int
	// Format is NV12, P010 or BGRA8.
	Format capture.Format
	// Width and Height are the picture's visible size; the surface may be
	// larger (coded size, aligned).
	Width, Height int
	PTS           int64
	// Latency is the time from Decode's call to the picture being ready.
	Latency time.Duration
	// Ref is the backend's handle that keeps Surface alive (an IMFSample*,
	// a CVPixelBufferRef). Callers leave it alone.
	Ref uintptr
}

// ErrNoFrame means the access unit produced no picture yet (the decoder
// needs more input). It is not a failure.
var ErrNoFrame = errors.New("decode: no picture for this access unit")

// ErrUnsupported means no hardware decoder takes this codec or format.
var ErrUnsupported = errors.New("decode: unsupported by this decoder")

// Config is what the caller knows of the stream before the first access
// unit. Width and Height are hints; the bitstream's own size wins.
type Config struct {
	Codec         encode.Codec
	Width, Height int
	FPS           int
	// Chroma and ChunkLen describe a PyroWave stream: its sampling and
	// depth, and the transport shard size its chunks are framed in.
	Chroma   encode.Chroma
	ChunkLen int
}

// Decoder turns access units into GPU pictures.
type Decoder interface {
	// Decode decodes one access unit (Annex B for HEVC, a temporal unit of
	// OBUs for AV1) and returns its picture.
	Decode(au []byte, pts int64) (Frame, error)
	// Release gives a picture back to the decoder's pool.
	Release(Frame)
	Close() error
}

// Target is the GPU a decoder writes its pictures on: the presenting
// surface's, so pictures never leave it.
type Target struct {
	// Device is an ID3D11Device* on Windows, an id<MTLDevice> on macOS.
	Device uintptr
	// Queue is the surface's id<MTLCommandQueue> on macOS, shared so decode
	// and present run in order without a fence. 0 on Windows.
	Queue   uintptr
	Adapter gpu.Adapter
}

// Backend opens a decoder for one codec on a Target.
type Backend struct {
	Name  string
	Codec encode.Codec
	Open  func(t Target, cfg Config) (Decoder, error)
}

var (
	backendsMu sync.RWMutex
	backends   = map[encode.Codec]Backend{}
)

// Register makes b the decoder for its codec.
func Register(b Backend) {
	backendsMu.Lock()
	defer backendsMu.Unlock()
	backends[b.Codec] = b
}

// Open opens the decoder registered for cfg.Codec on t.
func Open(t Target, cfg Config) (Decoder, error) {
	backendsMu.RLock()
	b, ok := backends[cfg.Codec]
	backendsMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: no decoder registered for %s", ErrUnsupported, cfg.Codec)
	}
	d, err := b.Open(t, cfg)
	if err != nil {
		return nil, fmt.Errorf("decode: %s: %w", b.Name, err)
	}
	return d, nil
}
