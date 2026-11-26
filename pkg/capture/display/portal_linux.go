//go:build linux && !android && (amd64 || arm64)

package display

import (
	"errors"

	"github.com/planckdotgg/planck/pkg/capture"
)

var errNoPortal = errors.New("display: desktop portal capture is not built yet")

func portalDisplays() ([]capture.Display, error) { return nil, errNoPortal }

func openPortal(string, Options) (*Source, error) { return nil, errNoPortal }
