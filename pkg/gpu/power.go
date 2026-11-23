package gpu

// PowerMode is what AcquireHighPerf achieved on an adapter.
type PowerMode uint8

const (
	// PowerUnchanged: no policy was applied; Reason says why.
	PowerUnchanged PowerMode = iota
	// PowerNotNeeded: the vendor's encoder does not drop to an idle clock
	// under a paced load, so there is nothing to change.
	PowerNotNeeded
	// PowerMaxPerformance: the driver keeps the adapter at its highest
	// performance state while this process runs.
	PowerMaxPerformance
)

func (m PowerMode) String() string {
	switch m {
	case PowerNotNeeded:
		return "not needed"
	case PowerMaxPerformance:
		return "max performance"
	}
	return "unchanged"
}

// PowerGuard holds a GPU power policy until Close. It never fails to
// acquire: when the policy cannot be applied (no NVIDIA driver, no rights to
// write driver settings) Mode is PowerUnchanged and Reason holds the cause,
// and the encoder runs at the driver's default clocks.
type PowerGuard struct {
	Adapter Adapter
	Mode    PowerMode
	Reason  error
	// Executable is the program name the policy is bound to.
	Executable string
	restore    func() error
}

// Close restores what AcquireHighPerf changed. It is safe to call twice.
func (g *PowerGuard) Close() error {
	if g == nil || g.restore == nil {
		return nil
	}
	r := g.restore
	g.restore = nil
	return r()
}
