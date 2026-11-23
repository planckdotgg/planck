//go:build !windows

package tailscale

import (
	"context"
	"errors"
	"net"
)

func dialPipe(context.Context, string) (net.Conn, error) {
	return nil, errors.New("tailscale: named pipes exist only on Windows")
}
