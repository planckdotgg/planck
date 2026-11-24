//go:build !windows && !darwin

package player

import (
	"errors"

	"github.com/egoist/mygo/gpusurface"

	"github.com/planckdotgg/planck/pkg/decode"
)

var errNoBackend = errors.New("player: no hardware decoder on this platform")

func openDecoder(*gpusurface.Surface, decode.Config) (decode.Decoder, error) {
	return nil, errNoBackend
}

func present(*gpusurface.Surface, decode.Frame) error { return errNoBackend }

// observeGlass has nothing to report: the surface cannot tell when a
// frame reached the screen.
func observeGlass(*gpusurface.Surface, Observer) {}
