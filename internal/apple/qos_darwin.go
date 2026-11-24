//go:build darwin

package apple

import "fmt"

// QoSUserInteractive is QOS_CLASS_USER_INTERACTIVE of <sys/qos.h>.
const QoSUserInteractive = 0x21

var pthreadSetQoSClassSelf func(class uint32, relPriority int32) int32

func init() {
	Register(&pthreadSetQoSClassSelf, System, "pthread_set_qos_class_self_np")
}

// SetThreadQoS sets the calling OS thread's quality of service class, so
// the scheduler keeps it on the performance cores under load. The caller
// must hold runtime.LockOSThread for as long as the class should last.
func SetThreadQoS(class uint32) error {
	if r := pthreadSetQoSClassSelf(class, 0); r != 0 {
		return fmt.Errorf("apple: pthread_set_qos_class_self_np(%#x): errno %d", class, r)
	}
	return nil
}
