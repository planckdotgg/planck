//go:build windows

package gpu

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"unsafe"
)

// NVIDIA GPUs stay in P8 under a paced encode load (Phase 0: HEVC 1080p
// 2.7-3.3 ms p50 paced against 1.3-1.4 ms boosted). The driver setting
// "Power management mode" = "Prefer maximum performance", bound to this
// executable in a driver settings (DRS) profile, keeps them boosted. This is
// the setting Sunshine applies to sunshine.exe.

const (
	nvapiOK                = 0
	nvapiSettingNotFound   = -160
	nvapiProfileNotFound   = -163
	nvapiExecutableMissing = -166

	preferredPStateID      = 0x1057eb71
	preferredPStatePrefMax = 1

	drsDWORD           = 0
	drsCurrentLocation = 0

	unicodeMax          = 2048
	sizeDRSSetting      = 12320 // NVDRS_SETTING_V1, pack 4
	sizeDRSProfile      = 4116  // NVDRS_PROFILE_V1
	sizeDRSApplication  = 12296 // NVDRS_APPLICATION_V1
	settingID           = 4100
	settingType         = 4104
	settingLocation     = 4108
	settingCurrentValue = 8220
	appName             = 8
	appFriendlyName     = 8 + 2*unicodeMax
	profileName         = 4

	powerProfileName = "Planck"
)

func nvVersion(size, v uint32) uint32 { return size | v<<16 }

// NVAPI interface IDs (nvapi_interface.h).
const (
	idInitialize            = 0x0150e828
	idGetErrorMessage       = 0x6c2d048c
	idCreateSession         = 0x0694d52e
	idDestroySession        = 0xdad9cff8
	idLoadSettings          = 0x375dbd6b
	idSaveSettings          = 0xfcbc7e14
	idCreateProfile         = 0xcc176068
	idDeleteProfile         = 0x17093206
	idFindProfileByName     = 0x7e4a9a0b
	idCreateApplication     = 0x4347a9de
	idDeleteApplication     = 0x2c694bc6
	idFindApplicationByName = 0xeee566b2
	idSetSetting            = 0x577dd202
	idGetSetting            = 0x73bf8338
	idDeleteProfileSetting  = 0xe4a26362
)

// NVAPIError is a failed NVAPI call.
type NVAPIError struct {
	Call    string
	Status  int32
	Message string
}

func (e *NVAPIError) Error() string {
	return fmt.Sprintf("nvapi: %s: status %d (%s)", e.Call, e.Status, e.Message)
}

var (
	nvapiOnce  sync.Once
	nvapiQuery uintptr
	nvapiErr   error
	// drsMu serializes DRS sessions: two guards in one process must not
	// interleave a read-modify-save of the same profile.
	drsMu sync.Mutex
)

func loadNVAPI() error {
	nvapiOnce.Do(func() {
		dll := syscall.NewLazyDLL("nvapi64.dll")
		if err := dll.Load(); err != nil {
			nvapiErr = fmt.Errorf("nvapi: %w", err)
			return
		}
		p := dll.NewProc("nvapi_QueryInterface")
		if err := p.Find(); err != nil {
			nvapiErr = fmt.Errorf("nvapi: %w", err)
			return
		}
		nvapiQuery = p.Addr()
		if st := nvCall(idInitialize); st != nvapiOK {
			nvapiErr = nvError("NvAPI_Initialize", st)
		}
	})
	return nvapiErr
}

//go:uintptrescapes
func nvCall(id uint32, args ...uintptr) int32 {
	fn, _, _ := syscall.SyscallN(nvapiQuery, uintptr(id))
	if fn == 0 {
		return -3 // NVAPI_NO_IMPLEMENTATION
	}
	r, _, _ := syscall.SyscallN(fn, args...)
	return int32(r)
}

func nvError(call string, st int32) error {
	var msg [64]byte
	nvCall(idGetErrorMessage, uintptr(st), uintptr(unsafe.Pointer(&msg[0])))
	n := 0
	for n < len(msg) && msg[n] != 0 {
		n++
	}
	return &NVAPIError{Call: call, Status: st, Message: string(msg[:n])}
}

func putUnicode(b []byte, off int, s string) {
	u, _ := syscall.UTF16FromString(s)
	if len(u) > unicodeMax {
		u = append(u[:unicodeMax-1], 0)
	}
	copy(unsafe.Slice((*uint16)(unsafe.Pointer(&b[off])), unicodeMax), u)
}

func unicode(s string) []byte {
	b := make([]byte, 2*unicodeMax)
	putUnicode(b, 0, s)
	return b
}

func u32(b []byte, off int) uint32      { return *(*uint32)(unsafe.Pointer(&b[off])) }
func put32(b []byte, off int, v uint32) { *(*uint32)(unsafe.Pointer(&b[off])) = v }
func addr(b []byte) uintptr             { return uintptr(unsafe.Pointer(&b[0])) }

// drsSession is one NVAPI driver settings session.
type drsSession struct{ h uintptr }

func openDRS() (*drsSession, error) {
	if err := loadNVAPI(); err != nil {
		return nil, err
	}
	s := &drsSession{}
	if st := nvCall(idCreateSession, uintptr(unsafe.Pointer(&s.h))); st != nvapiOK {
		return nil, nvError("NvAPI_DRS_CreateSession", st)
	}
	if st := nvCall(idLoadSettings, s.h); st != nvapiOK {
		s.close()
		return nil, nvError("NvAPI_DRS_LoadSettings", st)
	}
	return s, nil
}

func (s *drsSession) close() { nvCall(idDestroySession, s.h) }

func (s *drsSession) save() error {
	if st := nvCall(idSaveSettings, s.h); st != nvapiOK {
		return nvError("NvAPI_DRS_SaveSettings", st)
	}
	return nil
}

// pstate reads the profile's own PREFERRED_PSTATE value; ok is false when
// the profile inherits it.
func (s *drsSession) pstate(profile uintptr) (value uint32, ok bool, err error) {
	st := make([]byte, sizeDRSSetting)
	put32(st, 0, nvVersion(sizeDRSSetting, 1))
	switch r := nvCall(idGetSetting, s.h, profile, preferredPStateID, addr(st)); r {
	case nvapiOK:
		if u32(st, settingLocation) != drsCurrentLocation {
			return 0, false, nil
		}
		return u32(st, settingCurrentValue), true, nil
	case nvapiSettingNotFound:
		return 0, false, nil
	default:
		return 0, false, nvError("NvAPI_DRS_GetSetting", r)
	}
}

func (s *drsSession) setPState(profile uintptr, v uint32) error {
	st := make([]byte, sizeDRSSetting)
	put32(st, 0, nvVersion(sizeDRSSetting, 1))
	put32(st, settingID, preferredPStateID)
	put32(st, settingType, drsDWORD)
	put32(st, settingLocation, drsCurrentLocation)
	put32(st, settingCurrentValue, v)
	if r := nvCall(idSetSetting, s.h, profile, addr(st)); r != nvapiOK {
		return nvError("NvAPI_DRS_SetSetting", r)
	}
	return nil
}

// undo records what applyPolicy changed so restore can put it back.
type undo struct {
	exe            string
	profile        string
	createdProfile bool
	createdApp     bool
	changedSetting bool
	hadValue       bool
	oldValue       uint32
}

// AcquireHighPerf keeps adapter at its highest performance state while this
// process runs. On NVIDIA it writes the driver profile setting before the
// caller creates its D3D11 device and encoder, which is when the driver
// reads it; AMD, Intel and Apple encoders do not need it.
func AcquireHighPerf(adapter Adapter) *PowerGuard {
	g := &PowerGuard{Adapter: adapter}
	if adapter.Vendor != VendorNVIDIA {
		g.Mode = PowerNotNeeded
		return g
	}
	exe, err := os.Executable()
	if err != nil {
		g.Reason = err
		return g
	}
	g.Executable = filepath.Base(exe)
	drsMu.Lock()
	defer drsMu.Unlock()
	u, err := applyPolicy(g.Executable)
	if err != nil {
		g.Reason = err
		return g
	}
	g.Mode = PowerMaxPerformance
	g.restore = func() error {
		drsMu.Lock()
		defer drsMu.Unlock()
		return restorePolicy(u)
	}
	return g
}

func applyPolicy(exe string) (*undo, error) {
	s, err := openDRS()
	if err != nil {
		return nil, err
	}
	defer s.close()
	u := &undo{exe: exe}
	var profile uintptr
	app := make([]byte, sizeDRSApplication)
	put32(app, 0, nvVersion(sizeDRSApplication, 1))
	// An executable can belong to one profile only: when the driver already
	// lists it (a game profile, or ours from a run that crashed), set the
	// power mode there.
	switch r := nvCall(idFindApplicationByName, s.h, addr(unicode(exe)), uintptr(unsafe.Pointer(&profile)), addr(app)); r {
	case nvapiOK:
		u.profile = profileNameOf(s, profile)
		if u.profile == powerProfileName {
			// Left by a run that never closed its guard: ours to remove.
			u.createdApp = true
		}
	case nvapiExecutableMissing:
		if r := nvCall(idFindProfileByName, s.h, addr(unicode(powerProfileName)), uintptr(unsafe.Pointer(&profile))); r == nvapiProfileNotFound {
			p := make([]byte, sizeDRSProfile)
			put32(p, 0, nvVersion(sizeDRSProfile, 1))
			putUnicode(p, profileName, powerProfileName)
			if r := nvCall(idCreateProfile, s.h, addr(p), uintptr(unsafe.Pointer(&profile))); r != nvapiOK {
				return nil, nvError("NvAPI_DRS_CreateProfile", r)
			}
			u.createdProfile = true
		} else if r != nvapiOK {
			return nil, nvError("NvAPI_DRS_FindProfileByName", r)
		}
		u.profile = powerProfileName
		clear(app)
		put32(app, 0, nvVersion(sizeDRSApplication, 1))
		putUnicode(app, appName, exe)
		putUnicode(app, appFriendlyName, exe)
		if r := nvCall(idCreateApplication, s.h, profile, addr(app)); r != nvapiOK {
			return nil, nvError("NvAPI_DRS_CreateApplication", r)
		}
		u.createdApp = true
	default:
		return nil, nvError("NvAPI_DRS_FindApplicationByName", r)
	}
	v, ok, err := s.pstate(profile)
	if err != nil {
		return nil, err
	}
	u.hadValue, u.oldValue = ok, v
	u.changedSetting = !ok || v != preferredPStatePrefMax
	if u.changedSetting {
		if err := s.setPState(profile, preferredPStatePrefMax); err != nil {
			return nil, err
		}
	}
	if !u.changedSetting && !u.createdApp {
		return u, nil
	}
	if err := s.save(); err != nil {
		return nil, err
	}
	return u, nil
}

func profileNameOf(s *drsSession, profile uintptr) string {
	const idGetProfileInfo = 0x61cd6fd6
	p := make([]byte, sizeDRSProfile)
	put32(p, 0, nvVersion(sizeDRSProfile, 1))
	if nvCall(idGetProfileInfo, s.h, profile, addr(p)) != nvapiOK {
		return ""
	}
	return syscall.UTF16ToString(unsafe.Slice((*uint16)(unsafe.Pointer(&p[profileName])), unicodeMax))
}

func restorePolicy(u *undo) error {
	if !u.createdProfile && !u.createdApp && !u.changedSetting {
		return nil
	}
	s, err := openDRS()
	if err != nil {
		return err
	}
	defer s.close()
	var profile uintptr
	switch r := nvCall(idFindProfileByName, s.h, addr(unicode(u.profile)), uintptr(unsafe.Pointer(&profile))); r {
	case nvapiOK:
	case nvapiProfileNotFound:
		// Another Planck process that created the profile already removed it.
		return nil
	default:
		return nvError("NvAPI_DRS_FindProfileByName", r)
	}
	if u.createdProfile {
		if r := nvCall(idDeleteProfile, s.h, profile); r != nvapiOK {
			return nvError("NvAPI_DRS_DeleteProfile", r)
		}
		return s.save()
	}
	if u.createdApp {
		if r := nvCall(idDeleteApplication, s.h, profile, addr(unicode(u.exe))); r != nvapiOK {
			return nvError("NvAPI_DRS_DeleteApplication", r)
		}
	}
	if u.changedSetting {
		if u.hadValue {
			if err := s.setPState(profile, u.oldValue); err != nil {
				return err
			}
		} else if r := nvCall(idDeleteProfileSetting, s.h, profile, preferredPStateID); r != nvapiOK && r != nvapiSettingNotFound {
			return nvError("NvAPI_DRS_DeleteProfileSetting", r)
		}
	}
	return s.save()
}

// ReadPState reports the PREFERRED_PSTATE value the driver holds for exe,
// and whether exe's profile sets it. Tests use it to check Close restored it.
func ReadPState(exe string) (value uint32, set bool, err error) {
	drsMu.Lock()
	defer drsMu.Unlock()
	s, err := openDRS()
	if err != nil {
		return 0, false, err
	}
	defer s.close()
	var profile uintptr
	app := make([]byte, sizeDRSApplication)
	put32(app, 0, nvVersion(sizeDRSApplication, 1))
	r := nvCall(idFindApplicationByName, s.h, addr(unicode(exe)), uintptr(unsafe.Pointer(&profile)), addr(app))
	if r == nvapiExecutableMissing {
		return 0, false, nil
	}
	if r != nvapiOK {
		return 0, false, nvError("NvAPI_DRS_FindApplicationByName", r)
	}
	return s.pstate(profile)
}
