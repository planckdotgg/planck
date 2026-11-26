package session

import (
	"testing"

	"github.com/planckdotgg/planck/pkg/transport"
)

func TestLANPacingQueueForMacClients(t *testing.T) {
	if q := lanPacing(2500, "darwin").Queue; q != transport.MacReceiveQueue {
		t.Fatalf("macOS client: queue %+v, want %+v", q, transport.MacReceiveQueue)
	}
	for _, os := range []string{"windows", "linux", ""} {
		if q := lanPacing(2500, os).Queue; q != (transport.ReceiveQueue{}) {
			t.Fatalf("%q client: queue %+v, want none", os, q)
		}
	}
}
