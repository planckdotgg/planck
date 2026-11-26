//go:build darwin || linux || freebsd

package discovery

import (
	"syscall"

	"golang.org/x/sys/unix"
)

// shareAddr lets other sockets bind the discovery port: SO_REUSEPORT on a
// BSD stack, where every socket on a port needs it, with SO_REUSEADDR.
// syscall has no SO_REUSEPORT on linux/amd64, so it comes from x/sys/unix.
func shareAddr(_, _ string, c syscall.RawConn) error {
	var serr error
	err := c.Control(func(fd uintptr) {
		if serr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEADDR, 1); serr == nil {
			serr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEPORT, 1)
		}
	})
	if err != nil {
		return err
	}
	return serr
}
