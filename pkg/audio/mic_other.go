//go:build !darwin && !windows

package audio

// Linux has no backend yet. PipeWire's Pulse protocol fits these calls
// as they are: NewCapturer records a monitor of the default sink,
// NewMicCapturer the default source, and FindVirtualMic/OpenMicSink a
// module-null-sink whose monitor feeds a module-virtual-source.

func NewCapturer(CaptureOptions) (Capturer, error) { return nil, ErrUnsupported }
func NewMicCapturer() (MicCapturer, error)         { return nil, ErrUnsupported }
func OpenMicSink(VirtualMic) (MicSink, error)      { return nil, ErrUnsupported }
func outputDevices() ([]VirtualMic, error)         { return nil, ErrUnsupported }
func prepareVirtualMic(*VirtualMic) error          { return nil }

func NewMicCapturerOn(string) (MicCapturer, error) { return nil, ErrUnsupported }
func OutputDevices() ([]Device, error)             { return nil, ErrUnsupported }
func InputDevices() ([]Device, error)              { return nil, ErrUnsupported }
func MuteOutput() (func() error, error)            { return nil, ErrUnsupported }
func RunningApps() ([]App, error)                  { return nil, ErrUnsupported }
