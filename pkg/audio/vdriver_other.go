//go:build !darwin && !windows

package audio

const (
	vdriverInstallHint  = "Planck Virtual Audio is for Windows and macOS hosts."
	vdriverInactiveHint = vdriverInstallHint
)

func planckDriverInstalled() bool { return false }
