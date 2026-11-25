//go:build windows

package audio

import "golang.org/x/sys/windows/registry"

const (
	vdriverInstallHint  = "Install Planck Virtual Audio (drivers/audio/windows) so apps here hear a client's microphone."
	vdriverInactiveHint = "Planck Virtual Audio is installed but its speaker is not active. Check it in Device Manager or Sound settings, or restart this PC."
)

// planckDriverInstalled looks for the service PlanckVAD.inf adds, which
// any user may read.
func planckDriverInstalled() bool {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Services\PlanckVAD`, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	k.Close()
	return true
}
