//go:build darwin

package audio

import (
	"fmt"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"

	"github.com/planckdotgg/planck/internal/apple"
)

func dlopen(path string) uintptr {
	h, err := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		panic(err)
	}
	return h
}

var (
	coreAudioLib    = dlopen("/System/Library/Frameworks/CoreAudio.framework/CoreAudio")
	audioToolboxLib = dlopen("/System/Library/Frameworks/AudioToolbox.framework/AudioToolbox")

	audioObjectGetPropertyDataSize func(obj uint32, addr *propertyAddress, qualSize uint32, qual unsafe.Pointer, size *uint32) int32
	audioObjectGetPropertyData     func(obj uint32, addr *propertyAddress, qualSize uint32, qual unsafe.Pointer, size *uint32, out unsafe.Pointer) int32

	audioQueueNewInput       func(format *streamBasicDescription, cb uintptr, user uintptr, runLoop, mode uintptr, flags uint32, out *uintptr) int32
	audioQueueNewOutput      func(format *streamBasicDescription, cb uintptr, user uintptr, runLoop, mode uintptr, flags uint32, out *uintptr) int32
	audioQueueAllocateBuffer func(aq uintptr, size uint32, out **audioQueueBuffer) int32
	audioQueueEnqueueBuffer  func(aq uintptr, buf *audioQueueBuffer, n uint32, descs uintptr) int32
	audioQueueStart          func(aq uintptr, at uintptr) int32
	audioQueueStop           func(aq uintptr, immediate bool) int32
	audioQueueDispose        func(aq uintptr, immediate bool) int32
	audioQueueSetProperty    func(aq uintptr, id uint32, data unsafe.Pointer, size uint32) int32
)

func init() {
	apple.Register(&audioObjectGetPropertyDataSize, coreAudioLib, "AudioObjectGetPropertyDataSize")
	apple.Register(&audioObjectGetPropertyData, coreAudioLib, "AudioObjectGetPropertyData")
	apple.Register(&audioQueueNewInput, audioToolboxLib, "AudioQueueNewInput")
	apple.Register(&audioQueueNewOutput, audioToolboxLib, "AudioQueueNewOutput")
	apple.Register(&audioQueueAllocateBuffer, audioToolboxLib, "AudioQueueAllocateBuffer")
	apple.Register(&audioQueueEnqueueBuffer, audioToolboxLib, "AudioQueueEnqueueBuffer")
	apple.Register(&audioQueueStart, audioToolboxLib, "AudioQueueStart")
	apple.Register(&audioQueueStop, audioToolboxLib, "AudioQueueStop")
	apple.Register(&audioQueueDispose, audioToolboxLib, "AudioQueueDispose")
	apple.Register(&audioQueueSetProperty, audioToolboxLib, "AudioQueueSetProperty")
}

func fourCC(s string) uint32 {
	return uint32(s[0])<<24 | uint32(s[1])<<16 | uint32(s[2])<<8 | uint32(s[3])
}

var (
	selDevices       = fourCC("dev#")
	selPIDToProcess  = fourCC("id2p")
	selName          = fourCC("lnam")
	selDeviceUID     = fourCC("uid ")
	selStreams       = fourCC("stm#")
	selTapFormat     = fourCC("tfmt")
	selDefaultOutput = fourCC("dOut")
	scopeGlobal      = fourCC("glob")
	scopeOutput      = fourCC("outp")
	aqCurrentDevice  = fourCC("aqcd")
	formatSignedInt  = uint32(1 << 2)
	formatPacked     = uint32(1 << 3)
)

const systemObject = 1

type propertyAddress struct {
	selector, scope, element uint32
}

// audioTimeStamp is the head of AudioTimeStamp: the fields before
// mHostTime and mHostTime itself.
type audioTimeStamp struct {
	sampleTime float64
	hostTime   uint64
}

func osStatus(op string, st int32) error {
	if st != 0 {
		return fmt.Errorf("audio: %s: OSStatus %d", op, st)
	}
	return nil
}

func getProp(obj uint32, sel, scope uint32, out unsafe.Pointer, size uint32) error {
	addr := propertyAddress{sel, scope, 0}
	return osStatus("AudioObjectGetPropertyData", audioObjectGetPropertyData(obj, &addr, 0, nil, &size, out))
}

// propString reads a CFString property and releases it.
func propString(obj uint32, sel, scope uint32) string {
	var s uintptr
	if getProp(obj, sel, scope, unsafe.Pointer(&s), uint32(unsafe.Sizeof(s))) != nil || s == 0 {
		return ""
	}
	defer apple.CFRelease(s)
	return goString(objc.ID(s))
}

func goString(s objc.ID) string {
	p := objc.Send[*byte](s, sel("UTF8String"))
	if p == nil {
		return ""
	}
	n := 0
	for *(*byte)(unsafe.Add(unsafe.Pointer(p), n)) != 0 {
		n++
	}
	return string(unsafe.Slice(p, n))
}

func deviceIDs() ([]uint32, error) {
	addr := propertyAddress{selDevices, scopeGlobal, 0}
	var size uint32
	if err := osStatus("devices size", audioObjectGetPropertyDataSize(systemObject, &addr, 0, nil, &size)); err != nil {
		return nil, err
	}
	ids := make([]uint32, size/4)
	if len(ids) == 0 {
		return nil, nil
	}
	if err := osStatus("devices", audioObjectGetPropertyData(systemObject, &addr, 0, nil, &size, unsafe.Pointer(&ids[0]))); err != nil {
		return nil, err
	}
	return ids[:size/4], nil
}

// outputDevices lists the devices with output streams, by UID and name.
func outputDevices() ([]VirtualMic, error) {
	ids, err := deviceIDs()
	if err != nil {
		return nil, err
	}
	var out []VirtualMic
	for _, id := range ids {
		addr := propertyAddress{selStreams, scopeOutput, 0}
		var size uint32
		if audioObjectGetPropertyDataSize(id, &addr, 0, nil, &size) != 0 || size == 0 {
			continue
		}
		if uid := propString(id, selDeviceUID, scopeGlobal); uid != "" {
			out = append(out, VirtualMic{ID: uid, Name: propString(id, selName, scopeGlobal)})
		}
	}
	return out, nil
}

// processObject is the Core Audio process object of pid, or 0 when the
// process has not touched audio.
func processObject(pid int32) uint32 {
	addr := propertyAddress{selPIDToProcess, scopeGlobal, 0}
	var obj uint32
	size := uint32(4)
	if audioObjectGetPropertyData(systemObject, &addr, 4, unsafe.Pointer(&pid), &size, unsafe.Pointer(&obj)) != 0 {
		return 0
	}
	return obj
}

func monoInt16Format() streamBasicDescription {
	return streamBasicDescription{sampleRate: SampleRate, formatID: audioFormatLinearPCM, formatFlags: formatSignedInt | formatPacked,
		bytesPerPacket: 2 * MicChannels, framesPerPacket: 1, bytesPerFrame: 2 * MicChannels, channelsPerFrame: MicChannels, bitsPerChannel: 16}
}

// audioQueueBuffer is the head of AudioQueueBuffer.
type audioQueueBuffer struct {
	capacity uint32
	_        uint32
	data     unsafe.Pointer
	size     uint32
}
