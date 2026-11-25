// Package power keeps this machine awake while it hosts, and locks it.
package power

import "errors"

var ErrUnsupported = errors.New("power: not supported on this platform")
