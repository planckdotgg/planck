//go:build !darwin && !windows

package rtprio

func boost() error { return nil }
