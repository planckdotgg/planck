package rtprio

import "github.com/planckdotgg/planck/internal/apple"

func boost() error { return apple.SetThreadQoS(apple.QoSUserInteractive) }
