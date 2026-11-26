package input

import (
	"strings"
)

// portalRequestPath is the object path a portal call with handle_token
// token answers on, for a caller whose bus name is sender.
func portalRequestPath(sender, token string) string {
	return "/org/freedesktop/portal/desktop/request/" + portalSender(sender) + "/" + token
}

func portalSender(sender string) string {
	return strings.ReplaceAll(strings.TrimPrefix(sender, ":"), ".", "_")
}

// Device types of RemoteDesktop.SelectDevices.
const (
	portalKeyboard = 1
	portalPointer  = 2
)

// Axes of RemoteDesktop.NotifyPointerAxisDiscrete.
const (
	portalAxisVertical   = 0
	portalAxisHorizontal = 1
)

// portalSteps converts a WheelNotch-unit delta to the discrete steps the
// portal takes, carrying the remainder and flipping the sign: the portal
// counts steps toward the user as positive.
func portalSteps(delta int16, acc *wheelAccum) int32 { return -acc.lines(delta) }
