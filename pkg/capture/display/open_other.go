//go:build !windows && !darwin

package display

import (
	"errors"

	"github.com/watzon/planck/pkg/capture"
)

var errNoBackend = errors.New("display: no capture backend on this platform")

func List() ([]capture.Display, error) { return nil, errNoBackend }

func Open(id string, opts Options) (*Source, error) { return nil, errNoBackend }
