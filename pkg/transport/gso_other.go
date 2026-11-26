//go:build !windows

package transport

import "net"

// Linux has the same offload as UDP_SEGMENT (a uint16 segment size at
// level SOL_UDP); it is not wired up because no Linux host has measured it.
const segmentOOBLen = 0

func gsoSupported(*net.UDPConn) bool { return false }

func putSegmentOOB([]byte, int) {}
