// Package audio carries Planck's game audio: capture on the host, Opus
// encode and decode, a jitter buffer on the client, and playback. All PCM is
// interleaved 48 kHz stereo int16.
package audio

import (
	"context"
	"errors"
)

const (
	SampleRate = 48000
	Channels   = 2
)

// Capturer records what the host is playing.
type Capturer interface {
	// Start begins capture and returns once the device is running. onPCM
	// receives interleaved stereo samples and the time of the first sample
	// in nanoseconds on the platform capture clock (mach absolute time on
	// macOS, QueryPerformanceCounter on Windows), the clock video frames are
	// stamped with. The stamps differ by platform: ScreenCaptureKit's lag
	// delivery by about 25ms, while WASAPI loopback reports the endpoint's
	// play-out time, about 27ms after delivery on bigboi. pcm is reused
	// after onPCM returns. Capture stops when ctx ends or Close is called.
	Start(ctx context.Context, onPCM func(pcm []int16, pts int64)) error
	Close() error
}

// CaptureOptions tunes desktop audio capture. Every backend leaves out
// Planck's own process, so the host never hears what it plays itself.
type CaptureOptions struct {
	// ExcludePIDs are more processes to leave out, such as a voice chat
	// app. The macOS process tap honours them; Windows process loopback
	// takes a single process tree, so there they are not supported.
	ExcludePIDs []int
}

// Capture paths a capturer's Mode reports.
const (
	ModeProcessLoopback  = "process-loopback"  // Windows, without Planck's process tree
	ModeEndpointLoopback = "endpoint-loopback" // Windows default render endpoint, Planck included
	ModeProcessTap       = "process-tap"       // macOS Core Audio tap, without Planck
	ModeScreenCaptureKit = "screencapturekit"  // macOS, without Planck
	ModeMicrophone       = "microphone"
)

// Player plays decoded audio on the client.
type Player interface {
	Write(pcm []int16) error
	Close() error
}

var ErrClosed = errors.New("audio: closed")
