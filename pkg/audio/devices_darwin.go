//go:build darwin

package audio

import (
	"errors"
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/planckdotgg/planck/internal/apple"
)

var audioObjectSetPropertyData func(obj uint32, addr *propertyAddress, qualSize uint32, qual unsafe.Pointer, size uint32, data unsafe.Pointer) int32

func init() {
	apple.Register(&audioObjectSetPropertyData, coreAudioLib, "AudioObjectSetPropertyData")
}

var (
	selDefaultInput = fourCC("dIn ")
	selMute         = fourCC("mute")
	scopeInput      = fourCC("inpt")
)

// OutputDevices lists the devices that play sound.
func OutputDevices() ([]Device, error) { return devicesWith(scopeOutput, selDefaultOutput) }

// InputDevices lists the devices that record sound, microphones among
// them.
func InputDevices() ([]Device, error) { return devicesWith(scopeInput, selDefaultInput) }

func devicesWith(scope, defaultSel uint32) ([]Device, error) {
	ids, err := deviceIDs()
	if err != nil {
		return nil, err
	}
	var def uint32
	_ = getProp(systemObject, defaultSel, scopeGlobal, unsafe.Pointer(&def), 4)
	var out []Device
	for _, id := range ids {
		addr := propertyAddress{selStreams, scope, 0}
		var size uint32
		if audioObjectGetPropertyDataSize(id, &addr, 0, nil, &size) != 0 || size == 0 {
			continue
		}
		if uid := propString(id, selDeviceUID, scopeGlobal); uid != "" {
			out = append(out, Device{ID: uid, Name: propString(id, selName, scopeGlobal), IsDefault: id == def})
		}
	}
	return out, nil
}

// MuteOutput mutes the default output device and returns what puts its
// mute back as it was.
func MuteOutput() (restore func() error, err error) {
	var dev uint32
	if err := getProp(systemObject, selDefaultOutput, scopeGlobal, unsafe.Pointer(&dev), 4); err != nil {
		return nil, err
	}
	if dev == 0 {
		return nil, errors.New("audio: no default output device")
	}
	// A device without a main mute has one per channel.
	for _, elems := range [][]uint32{{0}, {1, 2}} {
		var was []uint32
		for _, el := range elems {
			addr := propertyAddress{selMute, scopeOutput, el}
			var m uint32
			size := uint32(4)
			if audioObjectGetPropertyData(dev, &addr, 0, nil, &size, unsafe.Pointer(&m)) != 0 {
				break
			}
			was = append(was, m)
		}
		if len(was) != len(elems) {
			continue
		}
		set := func(vals []uint32) error {
			var errs []error
			for i, el := range elems {
				addr := propertyAddress{selMute, scopeOutput, el}
				v := vals[i]
				errs = append(errs, osStatus("set mute", audioObjectSetPropertyData(dev, &addr, 0, nil, 4, unsafe.Pointer(&v))))
			}
			return errors.Join(errs...)
		}
		on := make([]uint32, len(elems))
		for i := range on {
			on[i] = 1
		}
		if err := set(on); err != nil {
			set(was)
			return nil, err
		}
		return func() error { return set(was) }, nil
	}
	return nil, fmt.Errorf("audio: %q cannot be muted", propString(dev, selName, scopeGlobal))
}

// RunningApps lists this user's processes by their short name, the first
// 16 bytes of it, as the kernel keeps it.
func RunningApps() ([]App, error) {
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.uid", int(unix.Getuid()))
	if err != nil {
		return nil, fmt.Errorf("audio: list processes: %w", err)
	}
	out := make([]App, 0, len(procs))
	for _, p := range procs {
		name := unix.ByteSliceToString(p.Proc.P_comm[:])
		if name != "" && p.Proc.P_pid > 0 {
			out = append(out, App{PID: int(p.Proc.P_pid), Name: strings.TrimSpace(name)})
		}
	}
	return out, nil
}
