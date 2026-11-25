//go:build darwin

package gamepad

import (
	"os"
	"testing"
)

// Runs the IOHIDManager path on headsets (Consumer page, usage 1), which
// most Macs have attached where a gamepad is not.
func TestLiveHIDPlumbing(t *testing.T) {
	if os.Getenv("PLANCK_LIVE_GAMEPAD_TEST") != "1" {
		t.Skip("set PLANCK_LIVE_GAMEPAD_TEST=1 to open real HID devices")
	}
	c, err := openHID([][2]uint32{{pageConsumer, 1}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	devs := c.Controllers()
	if len(devs) == 0 {
		t.Skip("no consumer-control HID device attached")
	}
	for _, info := range devs {
		var raw RawState
		err := c.Read(info.ID, &raw)
		t.Logf("%+v: read %v, %d buttons", info, err, len(raw.Buttons))
		if info.Name == "" || !ValidGUID(info.GUID) || info.Slot < 0 {
			t.Errorf("incomplete info %+v", info)
		}
	}
	if err := c.Read(-5, &RawState{}); err != ErrNotConnected {
		t.Errorf("Read of unknown id = %v", err)
	}
}
