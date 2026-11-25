//go:build darwin

package audio

import "os"

// planckDriverBundle is where drivers/audio/darwin installs the plug-in.
const planckDriverBundle = "/Library/Audio/Plug-Ins/HAL/PlanckAudio.driver"

const (
	vdriverInstallHint  = "Install Planck Virtual Audio (drivers/audio/darwin) so apps here hear a client's microphone."
	vdriverInactiveHint = "Planck Virtual Audio is installed but Core Audio has not loaded it. Restart Core Audio (sudo killall coreaudiod) or restart this Mac."
)

func planckDriverInstalled() bool {
	fi, err := os.Stat(planckDriverBundle)
	return err == nil && fi.IsDir()
}
