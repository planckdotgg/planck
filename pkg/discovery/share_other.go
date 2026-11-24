//go:build !darwin && !linux && !freebsd && !windows

package discovery

import "syscall"

func shareAddr(_, _ string, _ syscall.RawConn) error { return nil }
