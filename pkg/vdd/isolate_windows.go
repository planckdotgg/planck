//go:build windows

package vdd

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"syscall"
	"unsafe"
)

// sdcUseDatabaseCurrent applies the layout Windows stored for the
// displays connected now.
const sdcUseDatabaseCurrent = 0x0f

// IsolateDisplay leaves the display with this GDI name, such as
// `\\.\DISPLAY3`, the only one Windows shows, at the desktop's origin,
// turning the others off: privacy mode. restore brings back the layout
// before it. The change is not stored, so if the host dies first, its
// virtual display going away, or the next sign-in, brings the displays
// back as Windows stored them.
func IsolateDisplay(name string) (restore func() error, err error) {
	saved, err := queryActive()
	if err != nil {
		return nil, err
	}
	keep := -1
	for i := range saved.paths {
		if n, err := gdiName(&saved.paths[i]); err == nil && strings.EqualFold(n, name) {
			keep = i
		}
	}
	if keep < 0 {
		return nil, fmt.Errorf("vdd: privacy: %s is not on the desktop", name)
	}
	if len(saved.paths) == 1 {
		return func() error { return nil }, nil
	}
	paths, modes := soloConfig(saved, keep)
	if err := setDisplayConfig(paths, modes, sdcApply|sdcUseSuppliedDisplayConf|sdcAllowChanges); err != nil {
		return nil, fmt.Errorf("vdd: privacy: %w", err)
	}
	return func() error {
		err := setDisplayConfig(saved.paths, saved.modes, sdcApply|sdcUseSuppliedDisplayConf|sdcAllowChanges)
		if err == nil {
			return nil
		}
		// The virtual display went first: the layout Windows stored has
		// the others.
		if err2 := setDisplayConfig(nil, nil, sdcApply|sdcUseDatabaseCurrent); err2 != nil {
			return fmt.Errorf("vdd: privacy: restore: %w", errors.Join(err, err2))
		}
		return nil
	}, nil
}

// soloConfig is c with the keep path alone, its source at the origin and
// its modes renumbered; c is not changed.
func soloConfig(c displayConfig, keep int) ([]pathInfo, []modeInfo) {
	p := c.paths[keep]
	var modes []modeInfo
	if i := p.Source.ModeInfoIdx; i != modeIdxInvalid && int(i) < len(c.modes) {
		m := c.modes[i]
		if m.InfoType == modeInfoTypeSource {
			m.Union[3], m.Union[4] = 0, 0
		}
		p.Source.ModeInfoIdx = uint32(len(modes))
		modes = append(modes, m)
	}
	if i := p.Target.ModeInfoIdx; i != modeIdxInvalid && int(i) < len(c.modes) {
		p.Target.ModeInfoIdx = uint32(len(modes))
		modes = append(modes, c.modes[i])
	}
	return []pathInfo{p}, slices.Clip(modes)
}

func setDisplayConfig(paths []pathInfo, modes []modeInfo, flags uintptr) error {
	var pp, mp uintptr
	if len(paths) > 0 {
		pp = uintptr(unsafe.Pointer(&paths[0]))
	}
	if len(modes) > 0 {
		mp = uintptr(unsafe.Pointer(&modes[0]))
	}
	if r, _, _ := syscall.SyscallN(procSetDisplayConfig.Addr(), uintptr(len(paths)), pp, uintptr(len(modes)), mp, flags); r != 0 {
		return fmt.Errorf("SetDisplayConfig: %w", syscall.Errno(r))
	}
	return nil
}
