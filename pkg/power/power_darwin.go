//go:build darwin

package power

import (
	"fmt"
	"os/exec"
	"sync"

	"github.com/ebitengine/purego"

	"github.com/planckdotgg/planck/internal/apple"
)

var (
	iokitOnce sync.Once
	iokitErr  error

	ioPMAssertionCreateWithName func(kind uintptr, level uint32, name uintptr, id *uint32) int32
	ioPMAssertionRelease        func(id uint32) int32
)

func loadIOKit() error {
	iokitOnce.Do(func() {
		lib, err := purego.Dlopen("/System/Library/Frameworks/IOKit.framework/IOKit", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			iokitErr = fmt.Errorf("power: IOKit: %w", err)
			return
		}
		apple.Register(&ioPMAssertionCreateWithName, lib, "IOPMAssertionCreateWithName")
		apple.Register(&ioPMAssertionRelease, lib, "IOPMAssertionRelease")
	})
	return iokitErr
}

// KeepAwake keeps the display, and so the machine, from sleeping on idle
// until release, with an IOKit power assertion that pmset -g assertions
// lists under reason.
func KeepAwake(reason string) (release func(), err error) {
	if err := loadIOKit(); err != nil {
		return nil, err
	}
	const levelOn = 255
	kind, name := apple.String("PreventUserIdleDisplaySleep"), apple.String(reason)
	defer apple.CFRelease(kind)
	defer apple.CFRelease(name)
	var id uint32
	if st := ioPMAssertionCreateWithName(kind, levelOn, name, &id); st != 0 {
		return nil, fmt.Errorf("power: IOPMAssertionCreateWithName: 0x%x", uint32(st))
	}
	var once sync.Once
	return func() { once.Do(func() { ioPMAssertionRelease(id) }) }, nil
}

// LockScreen locks the screen at once, as Ctrl-Cmd-Q does, through the
// login framework, or by sleeping the display, which locks it when the
// user's settings ask for the password at once.
func LockScreen() error {
	lib, err := purego.Dlopen("/System/Library/PrivateFrameworks/login.framework/Versions/Current/login", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err == nil {
		if sym, err := purego.Dlsym(lib, "SACLockScreenImmediate"); err == nil {
			if r, _, _ := purego.SyscallN(sym); int32(r) == 0 {
				return nil
			}
		}
	}
	if out, err := exec.Command("/usr/bin/pmset", "displaysleepnow").CombinedOutput(); err != nil {
		return fmt.Errorf("power: lock the screen: %v: %s", err, out)
	}
	return nil
}
