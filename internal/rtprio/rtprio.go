// Package rtprio raises the scheduling class of the threads on Planck's
// media path: receive, decode and present on the client, receive and encode
// on the host.
package rtprio

import "runtime"

// Boost locks the calling goroutine to its OS thread and raises that
// thread's class: QOS_CLASS_USER_INTERACTIVE on macOS, the MMCSS "Games"
// task on Windows, where the first call also takes the process out of
// power throttling. The goroutine must return still locked, which ends the
// thread, so the class never reaches another goroutine. A failure leaves
// the thread locked at its normal class.
func Boost() error {
	runtime.LockOSThread()
	return boost()
}
