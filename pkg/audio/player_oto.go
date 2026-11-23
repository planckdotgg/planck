//go:build darwin || windows

package audio

import (
	"fmt"
	"sync"
	"time"

	"github.com/ebitengine/oto/v3"
)

type PlayerConfig struct {
	// DeviceBuffer is the device buffer oto asks the OS for (AudioQueue on
	// macOS, WASAPI shared mode on Windows). Zero selects 20ms; on an M5 Pro,
	// 10ms starved the AudioQueue, which then played audio about 6% slow.
	DeviceBuffer time.Duration
	// MaxQueue bounds audio written but not yet pulled by the device; older
	// samples are dropped past it. Zero selects 60ms.
	MaxQueue time.Duration
}

type PlayerStats struct {
	Queued  time.Duration // written but not yet handed to the OS
	Dropped uint64        // samples dropped to keep MaxQueue
}

// DevicePlayer plays PCM on the default output device through oto.
type DevicePlayer struct {
	ring   *pcmRing
	player *oto.Player
	mu     sync.Mutex
	closed bool
}

// oto allows one context per process; every DevicePlayer shares it, so the
// first DeviceBuffer wins.
var (
	otoOnce sync.Once
	otoCtx  *oto.Context
	otoErr  error
)

func samplesFor(d time.Duration) int {
	return int(int64(SampleRate)*int64(d)/int64(time.Second)) * Channels
}

func NewPlayer(cfg PlayerConfig) (*DevicePlayer, error) {
	if cfg.DeviceBuffer == 0 {
		cfg.DeviceBuffer = 20 * time.Millisecond
	}
	if cfg.MaxQueue == 0 {
		cfg.MaxQueue = 60 * time.Millisecond
	}
	otoOnce.Do(func() {
		var ready chan struct{}
		otoCtx, ready, otoErr = oto.NewContext(&oto.NewContextOptions{
			SampleRate:   SampleRate,
			ChannelCount: Channels,
			Format:       oto.FormatSignedInt16LE,
			BufferSize:   cfg.DeviceBuffer,
		})
		if otoErr == nil {
			<-ready
		}
	})
	if otoErr != nil {
		return nil, fmt.Errorf("audio: output device: %w", otoErr)
	}
	p := &DevicePlayer{ring: newPCMRing(samplesFor(cfg.MaxQueue))}
	p.player = otoCtx.NewPlayer(p)
	// oto reads ahead up to this many bytes. Less than 20ms let its mixer
	// run dry between 1ms source polls and insert silence.
	p.player.SetBufferSize(samplesFor(20*time.Millisecond) * 2)
	p.player.Play()
	return p, nil
}

// Read is the oto source; it is not for callers.
func (p *DevicePlayer) Read(b []byte) (int, error) { return p.ring.read(b), nil }

func (p *DevicePlayer) Write(pcm []int16) error {
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed {
		return ErrClosed
	}
	if err := otoCtx.Err(); err != nil {
		return fmt.Errorf("audio: output device: %w", err)
	}
	p.ring.write(pcm)
	return nil
}

func (p *DevicePlayer) Stats() PlayerStats {
	buffered := p.player.BufferedSize() / 2
	p.ring.mu.Lock()
	defer p.ring.mu.Unlock()
	return PlayerStats{
		Queued:  time.Duration((p.ring.n+buffered)/Channels) * time.Second / SampleRate,
		Dropped: p.ring.dropped,
	}
}

func (p *DevicePlayer) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	return p.player.Close()
}

var _ Player = (*DevicePlayer)(nil)
