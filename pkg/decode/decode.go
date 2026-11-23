// Package decode defines the hardware video decoder contract shared by the
// platform backends: Media Foundation on Direct3D 11 (Windows) and
// VideoToolbox (macOS). Decoded pictures stay on the GPU.
package decode

import (
	"errors"
	"time"

	"github.com/watzon/planck/pkg/capture"
	"github.com/watzon/planck/pkg/encode"
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
