package transport

// Mono returns monotonic nanoseconds since a process-local epoch. On Windows
// it reads QueryPerformanceCounter, because Go's monotonic clock there
// advances in timer-interrupt steps (about 0.5 ms), too coarse for packet
// pacing and latency measurement.
func Mono() int64 { return mono() }

// SleepUntilMono sleeps coarsely, then spins for the last spinThreshold.
func SleepUntilMono(deadline int64) {
	for {
		d := deadline - Mono()
		if d <= 0 {
			return
		}
		if d > int64(spinThreshold) {
			sleep(d - int64(spinThreshold))
			continue
		}
		gosched()
	}
}
