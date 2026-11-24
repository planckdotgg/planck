//go:build !darwin && !windows

package session

import (
	"errors"
	"time"

	"github.com/planckdotgg/planck/pkg/audio"
	"github.com/planckdotgg/planck/pkg/decode"
	"github.com/planckdotgg/planck/pkg/input"
)

var errNoBackend = errors.New("session: no capture, encode or decode backend on this platform yet")

func openDisplay(VideoParams) (VideoPipe, error)                    { return nil, errNoBackend }
func openSynth(VideoParams) (VideoPipe, error)                      { return nil, errNoBackend }
func newAudioCapturer(audio.CaptureOptions) (audio.Capturer, error) { return nil, errNoBackend }
func newInjector() (input.Injector, error)                          { return nil, errNoBackend }
func newAudioPlayer() (audio.Player, error)                         { return nil, errNoBackend }
func playerQueued(audio.Player) time.Duration                       { return 0 }
func OpenDecodeSink(decode.Config) (Sink, error)                    { return nil, errNoBackend }
func boostVideoThread() (func(), error)                             { return func() {}, nil }
