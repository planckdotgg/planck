//go:build linux && !android && (amd64 || arm64)

package session

import (
	"errors"
	"time"

	"github.com/planckdotgg/planck/pkg/audio"
	"github.com/planckdotgg/planck/pkg/decode"
	"github.com/planckdotgg/planck/pkg/decode/linuxdec"
	"github.com/planckdotgg/planck/pkg/input"
)

// The Linux host (capture, encode, input injection, audio) comes later;
// the client decodes with VA-API or NVDEC.
var errNoBackend = errors.New("session: no capture, encode or audio backend on Linux yet")

func openDisplay(VideoParams) (VideoPipe, error)                    { return nil, errNoBackend }
func openSynth(VideoParams) (VideoPipe, error)                      { return nil, errNoBackend }
func newAudioCapturer(audio.CaptureOptions) (audio.Capturer, error) { return nil, errNoBackend }
func newInjector() (input.Injector, error)                          { return nil, errNoBackend }
func newAudioPlayer() (audio.Player, error)                         { return nil, errNoBackend }
func playerQueued(audio.Player) time.Duration                       { return 0 }
func boostVideoThread() (func(), error)                             { return func() {}, nil }

// OpenDecodeSink decodes with VA-API or NVDEC and hands each picture
// straight back. A VA-API picture stays in its surface: the sink takes
// any DMA-BUF, so nothing is copied.
func OpenDecodeSink(cfg decode.Config) (Sink, error) {
	d, err := linuxdec.Open(decode.Target{Accepts: func(uint32, uint64) bool { return true }}, cfg)
	if err != nil {
		return nil, err
	}
	return &decodeSink{dec: d}, nil
}
