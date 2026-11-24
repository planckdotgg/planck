package audio

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"time"
)

// MicChannels is the channel count of microphone PCM: the client captures
// and the host plays mono.
const MicChannels = 1

// MicPCMFrameDuration is the frame of the raw PCM mic codec: 240 mono
// samples, 480 bytes, one datagram each.
const MicPCMFrameDuration = 5 * time.Millisecond

// VirtualMicHint is what a host without a virtual input device tells the
// client. Planck never installs one.
const VirtualMicHint = "Virtual mic not detected — install Steam Streaming Microphone / virtual audio device"

var (
	// ErrNoVirtualMic: no user-installed virtual input device matched.
	ErrNoVirtualMic = errors.New("audio: " + VirtualMicHint)
	// ErrVirtualMicSetup: a virtual mic was found but will not pass audio
	// as it is set up; the error says what to change.
	ErrVirtualMicSetup = errors.New("audio: virtual mic needs setup")
	// ErrUnsupported: this platform has no backend for the call yet.
	ErrUnsupported = errors.New("audio: not supported on this platform")
)

// MicCapturer records the client's default microphone as interleaved
// 48 kHz mono int16. Start and Close behave as Capturer's; pts is on the
// same clock.
type MicCapturer interface {
	Start(ctx context.Context, onPCM func(pcm []int16, pts int64)) error
	Close() error
}

// MicSink plays the client's microphone (48 kHz mono int16) into a virtual
// input device on the host, so voice chat and games hear it.
type MicSink interface {
	Player
	// Device is the name of the device apps record from.
	Device() string
}

// VirtualMic is a user-installed virtual audio device: Planck writes to
// its playback side (ID, Name) and apps record from its input side (Input).
type VirtualMic struct {
	ID    string
	Name  string
	Input string
	// inputGainDB is the input side's volume, which the sink takes off
	// what it writes when positive.
	inputGainDB float32
}

// pairedInput picks the input device fed by output: one whose name holds
// the part of output's name in parentheses ("Speakers (Steam Streaming
// Microphone)" feeds "Microphone (Steam Streaming Microphone)"), or the
// whole name. It returns -1 when none does.
func pairedInput(output string, inputs []string) int {
	key := output
	if i, j := strings.LastIndex(output, "("), strings.LastIndex(output, ")"); i >= 0 && j > i+1 {
		key = output[i+1 : j]
	}
	for i, n := range inputs {
		if strings.Contains(n, key) {
			return i
		}
	}
	return -1
}

// knownVirtualMics are playback-side device names, as substrings, of
// virtual input devices a user may have installed, best first.
var knownVirtualMics = []string{
	"Steam Streaming Microphone",
	"CABLE Input",
	"VB-Audio Virtual Cable",
	"BlackHole",
}

// pickVirtualMic chooses among the output devices' names: the first that
// contains want when it is set, else the best known virtual device. It
// returns -1 when none matches.
func pickVirtualMic(names []string, want string) int {
	if want != "" {
		for i, n := range names {
			if strings.Contains(strings.ToLower(n), strings.ToLower(want)) {
				return i
			}
		}
		return -1
	}
	for _, k := range knownVirtualMics {
		for i, n := range names {
			if strings.Contains(n, k) {
				return i
			}
		}
	}
	return -1
}

// FindVirtualMic looks for a virtual input device among the output devices:
// the one whose name contains want, or a known one when want is empty.
func FindVirtualMic(want string) (VirtualMic, error) {
	devs, err := outputDevices()
	if err != nil {
		return VirtualMic{}, err
	}
	names := make([]string, len(devs))
	for i, d := range devs {
		names[i] = d.Name
	}
	i := pickVirtualMic(names, want)
	if i < 0 {
		if want != "" {
			return VirtualMic{}, fmt.Errorf("%w (no output device matches %q)", ErrNoVirtualMic, want)
		}
		return VirtualMic{}, ErrNoVirtualMic
	}
	dev := devs[i]
	dev.Input = dev.Name
	if err := prepareVirtualMic(&dev); err != nil {
		return dev, err
	}
	return dev, nil
}

// PutPCM16 writes pcm as little-endian bytes into b and returns their
// length.
func PutPCM16(b []byte, pcm []int16) int {
	for i, v := range pcm {
		binary.LittleEndian.PutUint16(b[2*i:], uint16(v))
	}
	return 2 * len(pcm)
}

// PCMDecoder is the FrameDecoder of the raw PCM16 mic codec. It conceals a
// lost frame by repeating the last one at half volume each time; Playout
// fades to silence after a few.
type PCMDecoder struct {
	samples, channels int
	last              []int16
}

func NewPCMDecoder(frameDuration time.Duration, channels int) *PCMDecoder {
	n := FrameSamples(frameDuration)
	return &PCMDecoder{samples: n, channels: channels, last: make([]int16, n*channels)}
}

func (d *PCMDecoder) FrameSamples() int { return d.samples }
func (d *PCMDecoder) Channels() int     { return d.channels }

func (d *PCMDecoder) Decode(packet []byte, pcm []int16) (int, error) {
	n := d.samples * d.channels
	if len(packet) != 2*n {
		return 0, fmt.Errorf("audio: pcm packet of %d bytes, want %d", len(packet), 2*n)
	}
	for i := range n {
		pcm[i] = int16(binary.LittleEndian.Uint16(packet[2*i:]))
	}
	copy(d.last, pcm[:n])
	return d.samples, nil
}

func (d *PCMDecoder) Conceal(pcm []int16) (int, error) {
	for i := range d.last {
		d.last[i] /= 2
	}
	copy(pcm, d.last)
	return d.samples, nil
}
