package transport

import "golang.org/x/sys/windows"

func getsockoptInt(fd uintptr, opt int) (int, error) {
	return windows.GetsockoptInt(windows.Handle(fd), windows.SOL_SOCKET, opt)
}

const (
	soRcvBuf = windows.SO_RCVBUF
	soSndBuf = windows.SO_SNDBUF
)
