//go:build !darwin && !windows && !linux

package rtprio

func boost() error { return nil }
