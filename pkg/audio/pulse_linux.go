package audio

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jfreymuth/pulse"
	"github.com/jfreymuth/pulse/proto"
	"golang.org/x/sys/unix"
)

// pulseTimeout bounds each request to the sound server, so a hung server
// fails a call rather than blocking it.
const pulseTimeout = 5 * time.Second

// ModePulseMonitor is the capture path of Linux: the monitor of an output
// device, through PulseAudio or PipeWire's PulseAudio server.
const ModePulseMonitor = "pulse-monitor"

func newPulseClient() (*pulse.Client, error) {
	c, err := pulse.NewClient(pulse.ClientApplicationName("Planck"), pulse.ClientTimeout(pulseTimeout))
	if err != nil {
		return nil, fmt.Errorf("audio: sound server: %w", err)
	}
	return c, nil
}

// withPulse runs f on a connection that is closed after.
func withPulse[T any](f func(c *pulse.Client) (T, error)) (T, error) {
	c, err := newPulseClient()
	if err != nil {
		var zero T
		return zero, err
	}
	defer c.Close()
	return f(c)
}

// probePulse fails when no sound server answers, so that opening a capturer
// reports it at once rather than at Start.
func probePulse() error {
	c, err := newPulseClient()
	if err != nil {
		return err
	}
	c.Close()
	return nil
}

// monoNanos is CLOCK_MONOTONIC, the clock Linux video capture stamps frames
// with.
func monoNanos() int64 {
	var ts unix.Timespec
	_ = unix.ClockGettime(unix.CLOCK_MONOTONIC, &ts)
	return ts.Nano()
}

// pulseCapturer records one stream: the monitor of an output device (what
// the host plays) or a microphone.
type pulseCapturer struct {
	mic      bool
	deviceID string // a sink name for the monitor, a source name for a mic; "" is the default

	mu     sync.Mutex
	client *pulse.Client
	stream *pulse.RecordStream
	cancel context.CancelFunc
	closed bool
}

func (c *pulseCapturer) Mode() string {
	if c.mic {
		return ModeMicrophone
	}
	return ModePulseMonitor
}

// Start opens the stream. Chunks arrive about every 10 ms; each is stamped
// with the time its last sample was delivered, less its own length, on
// CLOCK_MONOTONIC. The server's own capture latency is not known to this
// client, so the stamps run slightly late.
func (c *pulseCapturer) Start(ctx context.Context, onPCM func(pcm []int16, pts int64)) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return ErrClosed
	}
	if c.stream != nil {
		return errors.New("audio: capturer already started")
	}
	client, err := newPulseClient()
	if err != nil {
		return err
	}
	channels := Channels
	if c.mic {
		channels = MicChannels
	}
	opts := []pulse.RecordOption{
		pulse.RecordSampleRate(SampleRate),
		pulse.RecordChannels(channelMap(channels)),
		pulse.RecordLatency(0.010),
		pulse.RecordMediaName("Planck"),
	}
	src, err := c.source(client)
	if err != nil {
		client.Close()
		return err
	}
	opts = append(opts, src)
	w := pulse.Int16Writer(func(pcm []int16) (int, error) {
		if n := len(pcm) / channels * channels; n > 0 {
			d := time.Duration(n/channels) * time.Second / SampleRate
			onPCM(pcm[:n], monoNanos()-int64(d))
		}
		return len(pcm), nil
	})
	stream, err := client.NewRecord(w, opts...)
	if err != nil {
		client.Close()
		return fmt.Errorf("audio: record: %w", err)
	}
	stream.Start()
	c.client, c.stream = client, stream
	ctx, c.cancel = context.WithCancel(ctx)
	go func() {
		<-ctx.Done()
		c.Close()
	}()
	return nil
}

func channelMap(n int) proto.ChannelMap {
	if n == 1 {
		return proto.ChannelMap{proto.ChannelMono}
	}
	return proto.ChannelMap{proto.ChannelLeft, proto.ChannelRight}
}

// source picks what to record: a named or default microphone, or the
// monitor of a named or default sink.
func (c *pulseCapturer) source(client *pulse.Client) (pulse.RecordOption, error) {
	if c.mic {
		if c.deviceID == "" {
			return func(*pulse.RecordStream) {}, nil
		}
		s, err := client.SourceByID(c.deviceID)
		if err != nil {
			return nil, fmt.Errorf("audio: input device %q: %w", c.deviceID, err)
		}
		return pulse.RecordSource(s), nil
	}
	var sink *pulse.Sink
	var err error
	if c.deviceID == "" {
		sink, err = client.DefaultSink()
	} else {
		sink, err = client.SinkByID(c.deviceID)
	}
	if err != nil {
		return nil, fmt.Errorf("audio: output device to record: %w", err)
	}
	return pulse.RecordMonitor(sink), nil
}

func (c *pulseCapturer) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	if c.cancel != nil {
		c.cancel()
	}
	if c.stream != nil {
		c.stream.Stop()
		c.stream.Close()
		c.client.Close()
	}
	return nil
}

// pulsePlayer plays PCM on one sink, stereo or mono, from a ring the
// caller fills. The server pulls; an empty ring plays silence.
type pulsePlayer struct {
	ring   *pcmRing
	client *pulse.Client
	stream *pulse.PlaybackStream
	mu     sync.Mutex
	closed bool
}

const pulsePlayQueue = 60 * time.Millisecond

// newPulsePlayer starts playback on sink (a sink name; "" is the default).
func newPulsePlayer(sink string, channels int) (*pulsePlayer, error) {
	client, err := newPulseClient()
	if err != nil {
		return nil, err
	}
	opts := []pulse.PlaybackOption{
		pulse.PlaybackSampleRate(SampleRate),
		pulse.PlaybackChannels(channelMap(channels)),
		pulse.PlaybackLatency(0.020),
		pulse.PlaybackMediaName("Planck"),
	}
	if sink != "" {
		s, err := client.SinkByID(sink)
		if err != nil {
			client.Close()
			return nil, fmt.Errorf("audio: output device %q: %w", sink, err)
		}
		opts = append(opts, pulse.PlaybackSink(s))
	}
	p := &pulsePlayer{client: client, ring: newPCMRing(samplesFor(pulsePlayQueue) / Channels * channels)}
	stream, err := client.NewPlayback(pulse.NewReader(readerFunc(p.read), proto.FormatInt16LE), opts...)
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("audio: playback: %w", err)
	}
	p.stream = stream
	stream.Start()
	return p, nil
}

type readerFunc func([]byte) (int, error)

func (f readerFunc) Read(b []byte) (int, error) { return f(b) }

// read always fills b: the server asks for a fixed amount and a short read
// would make its pull loop spin.
func (p *pulsePlayer) read(b []byte) (int, error) {
	n := p.ring.read(b)
	clear(b[n:])
	return len(b), nil
}

func (p *pulsePlayer) Write(pcm []int16) error {
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed {
		return ErrClosed
	}
	if p.stream.Closed() {
		if err := p.stream.Error(); err != nil {
			return fmt.Errorf("audio: output device: %w", err)
		}
		return ErrClosed
	}
	p.ring.write(pcm)
	return nil
}

func (p *pulsePlayer) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	p.stream.Stop()
	p.stream.Close()
	p.client.Close()
	return nil
}

// openOutput plays stereo PCM on output device d.
func openOutput(d Device) (Player, error) { return newPulsePlayer(d.ID, Channels) }
