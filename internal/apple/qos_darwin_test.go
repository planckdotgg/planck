package apple

import (
	"runtime"
	"testing"
)

func TestSetThreadQoS(t *testing.T) {
	var qosClassSelf func() uint32
	Register(&qosClassSelf, System, "qos_class_self")
	got := make(chan uint32, 1)
	go func() {
		runtime.LockOSThread()
		if err := SetThreadQoS(QoSUserInteractive); err != nil {
			t.Error(err)
		}
		got <- qosClassSelf()
	}()
	if c := <-got; c != QoSUserInteractive {
		t.Fatalf("qos_class_self = %#x, want %#x", c, QoSUserInteractive)
	}
}
