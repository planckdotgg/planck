//go:build darwin || windows

package gamepad

import (
	"os"
	"testing"
)

// Lists and reads the attached controllers. Runs only with
// PLANCK_LIVE_GAMEPAD_TEST=1.
func TestLiveCapture(t *testing.T) {
	if os.Getenv("PLANCK_LIVE_GAMEPAD_TEST") != "1" {
		t.Skip("set PLANCK_LIVE_GAMEPAD_TEST=1 to read real controllers")
	}
	c, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for _, info := range c.Controllers() {
		var raw RawState
		if err := c.Read(info.ID, &raw); err != nil {
			t.Errorf("%+v: Read: %v", info, err)
			continue
		}
		m := c.DefaultMapping(info.ID)
		t.Logf("%+v: %d buttons, %d axes, %d hats; state %+v; mapping %s", info, len(raw.Buttons), len(raw.Axes), len(raw.Hats), m.Apply(&raw), m.FormatSDLMapping())
	}
	t.Logf("virtual pads: %+v", VirtualPadStatus())
}
