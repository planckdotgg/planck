package rtprio

import (
	"sync"

	"github.com/planckdotgg/planck/internal/win"
)

var unthrottle sync.Once

func boost() error {
	var err error
	unthrottle.Do(func() { err = win.DisablePowerThrottling() })
	if _, berr := win.BoostThread("Games"); berr != nil {
		return berr
	}
	return err
}
