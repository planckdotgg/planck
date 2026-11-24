package rtprio

import "github.com/planckdotgg/planck/internal/win"

func boost() error {
	_, err := win.BoostThread("Games")
	return err
}
