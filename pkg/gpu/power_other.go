//go:build !windows

package gpu

// AcquireHighPerf applies no policy outside Windows: VideoToolbox on Apple
// silicon keeps its encode latency under a paced load (Phase 0).
func AcquireHighPerf(adapter Adapter) *PowerGuard {
	return &PowerGuard{Adapter: adapter, Mode: PowerNotNeeded}
}
