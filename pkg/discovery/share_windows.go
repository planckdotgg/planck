package discovery

import "syscall"

// shareAddr lets other sockets bind the discovery port. A socket bound
// without SO_REUSEADDR still holds the port alone.
func shareAddr(_, _ string, c syscall.RawConn) error {
	var serr error
	err := c.Control(func(fd uintptr) {
		serr = syscall.SetsockoptInt(syscall.Handle(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
	})
	if err != nil {
		return err
	}
	return serr
}
