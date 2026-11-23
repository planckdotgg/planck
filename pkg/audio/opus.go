package audio

import (
	"fmt"
	"time"

	"github.com/thesyncim/gopus"
)

const (
	DefaultFrameDuration = 10 * time.Millisecond
	DefaultBitrate       = 128000

	// MaxPacketSize is the largest single-frame Opus packet (RFC 6716 3.2.1).
	MaxPacketSize = 1275
)

// FrameSamples is the number of samples per channel in one frame of d.
func FrameSamples(d time.Duration) int { return int(int64(SampleRate) * int64(d) / int64(time.Second)) }

func checkFrameDuration(d time.Duration) error {
	if d != 10*time.Millisecond && d != 20*time.Millisecond {
		return fmt.Errorf("audio: frame duration %v: want 10ms or 20ms", d)
	}
	return nil
}

type EncoderConfig struct {
	FrameDuration time.Duration // 10ms or 20ms; zero selects DefaultFrameDuration
	Bitrate       int           // bits per second; zero selects DefaultBitrate
}

// Encoder turns 48 kHz stereo PCM frames into Opus packets. It uses the Opus
// low-delay application (CELT only), which has the smallest algorithmic delay.
// It is not safe for concurrent use.
type Encoder struct {
	enc     *gopus.Encoder
	samples int
}

func NewEncoder(cfg EncoderConfig) (*Encoder, error) {
	if cfg.FrameDuration == 0 {
		cfg.FrameDuration = DefaultFrameDuration
	}
	if cfg.Bitrate == 0 {
		cfg.Bitrate = DefaultBitrate
	}
	if err := checkFrameDuration(cfg.FrameDuration); err != nil {
		return nil, err
	}
	enc, err := gopus.NewEncoder(gopus.EncoderConfig{SampleRate: SampleRate, Channels: Channels, Application: gopus.ApplicationLowDelay})
	if err != nil {
		return nil, fmt.Errorf("audio: opus encoder: %w", err)
	}
	samples := FrameSamples(cfg.FrameDuration)
	if err := enc.SetFrameSize(samples); err != nil {
		return nil, fmt.Errorf("audio: opus frame size: %w", err)
	}
	if err := enc.SetBitrate(cfg.Bitrate); err != nil {
		return nil, fmt.Errorf("audio: opus bitrate: %w", err)
	}
	return &Encoder{enc: enc, samples: samples}, nil
}

// FrameSamples is the number of samples per channel Encode expects.
func (e *Encoder) FrameSamples() int { return e.samples }

// Encode compresses exactly one frame (FrameSamples()*Channels values) into
// packet and returns the packet length. packet should hold MaxPacketSize bytes.
func (e *Encoder) Encode(pcm []int16, packet []byte) (int, error) {
	if len(pcm) != e.samples*Channels {
		return 0, fmt.Errorf("audio: encode %d samples, want %d", len(pcm), e.samples*Channels)
	}
	n, err := e.enc.EncodeInt16(pcm, packet)
	if err != nil {
		return 0, fmt.Errorf("audio: opus encode: %w", err)
	}
	return n, nil
}

// Decoder turns Opus packets back into 48 kHz stereo PCM frames and conceals
// lost ones. It is not safe for concurrent use.
type Decoder struct {
	dec     *gopus.Decoder
	samples int
}

// NewDecoder makes a decoder for frames of frameDuration (10ms or 20ms;
// zero selects DefaultFrameDuration), the length Conceal fills.
func NewDecoder(frameDuration time.Duration) (*Decoder, error) {
	if frameDuration == 0 {
		frameDuration = DefaultFrameDuration
	}
	if err := checkFrameDuration(frameDuration); err != nil {
		return nil, err
	}
	dec, err := gopus.NewDecoder(gopus.DefaultDecoderConfig(SampleRate, Channels))
	if err != nil {
		return nil, fmt.Errorf("audio: opus decoder: %w", err)
	}
	return &Decoder{dec: dec, samples: FrameSamples(frameDuration)}, nil
}

func (d *Decoder) FrameSamples() int { return d.samples }

// Decode writes one frame into pcm, which must hold FrameSamples()*Channels
// values, and returns the samples per channel written.
func (d *Decoder) Decode(packet []byte, pcm []int16) (int, error) {
	if len(packet) == 0 {
		return 0, fmt.Errorf("audio: decode empty packet")
	}
	n, err := d.dec.DecodeInt16(packet, pcm[:d.samples*Channels])
	if err != nil {
		return 0, fmt.Errorf("audio: opus decode: %w", err)
	}
	return n, nil
}

// Conceal fills one frame of pcm with Opus packet-loss concealment.
func (d *Decoder) Conceal(pcm []int16) (int, error) {
	n, err := d.dec.DecodeInt16(nil, pcm[:d.samples*Channels])
	if err != nil {
		return 0, fmt.Errorf("audio: opus conceal: %w", err)
	}
	return n, nil
}
