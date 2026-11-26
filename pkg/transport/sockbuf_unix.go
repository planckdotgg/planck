//go:build !windows

package transport

import "syscall"

func getsockoptInt(fd uintptr, opt int) (int, error) {
	return syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, opt)
}

const (
	soRcvBuf = syscall.SO_RCVBUF
	soSndBuf = syscall.SO_SNDBUF
)
