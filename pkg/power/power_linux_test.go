package power

import (
	"os"
	"testing"
)

// TestKeepAwakeLinux takes and releases the inhibitors where a bus is
// there; it never locks the session.
func TestKeepAwakeLinux(t *testing.T) {
	if os.Getenv("DBUS_SESSION_BUS_ADDRESS") == "" {
		if _, err := os.Stat("/run/dbus/system_bus_socket"); err != nil {
			t.Skip("no D-Bus")
		}
	}
	release, err := KeepAwake("Planck power test")
	if err != nil {
		t.Skipf("no inhibitor service: %v", err)
	}
	release()
	release()
}
