//go:build !darwin && !windows

package linkinfo

import (
	"errors"
	"net"

	"github.com/planckdotgg/planck/pkg/defaults"
)

func interfaceLink(net.Interface) (defaults.Link, error) {
	return defaults.Link{}, errors.ErrUnsupported
}
