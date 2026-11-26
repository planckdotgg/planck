package ui

import (
	"slices"

	"github.com/planckdotgg/planck/pkg/discovery"
)

// fakeCalibration is the fake's calibration: StartCalibration records the
// host and runs nothing, the tests set state.
type fakeCalibration struct {
	starts, cancels []string
	startErr        error
	state           CalibrationState
	applied         []string
	resets          [][]discovery.PathKind
	status          map[string][]CalibrationSummary
}

func (f *fakeBackend) StartCalibration(hostID string) error {
	defer f.lock()()
	if f.cal.startErr != nil {
		return f.cal.startErr
	}
	f.cal.starts = append(f.cal.starts, hostID)
	f.cal.state = CalibrationState{Running: true, HostID: hostID, Phase: CalibratingNetwork}
	return nil
}

func (f *fakeBackend) CancelCalibration() {
	defer f.lock()()
	f.cal.cancels = append(f.cal.cancels, f.cal.state.HostID)
	f.cal.state.Running, f.cal.state.Cancelled = false, true
}

func (f *fakeBackend) Calibration() CalibrationState { defer f.lock()(); return f.cal.state }

func (f *fakeBackend) ApplyCalibration(hostID string) error {
	defer f.lock()()
	if r := f.cal.state.Result; r == nil || f.cal.state.HostID != hostID {
		return ErrNoCalibration
	}
	f.cal.applied = append(f.cal.applied, hostID)
	return nil
}

func (f *fakeBackend) ResetCalibration(hostID string, routes ...discovery.PathKind) error {
	defer f.lock()()
	f.cal.resets = append(f.cal.resets, routes)
	if len(routes) == 0 || f.cal.status == nil {
		delete(f.cal.status, hostID)
		return nil
	}
	f.cal.status[hostID] = slices.DeleteFunc(f.cal.status[hostID], func(s CalibrationSummary) bool { return slices.Contains(routes, s.PathKind) })
	return nil
}

func (f *fakeBackend) CalibrationStatus(hostID string) []CalibrationSummary {
	defer f.lock()()
	return slices.Clone(f.cal.status[hostID])
}
