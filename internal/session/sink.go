package session

import (
	"errors"
	"sync/atomic"

	"github.com/planckdotgg/planck/pkg/decode"
	"github.com/planckdotgg/planck/pkg/player"
)

// Sink takes the client's access units in order. *player.Player is a Sink
// that decodes into a window surface; OpenDecodeSink decodes without
// showing; NullSink only counts. Submit returns decode.ErrNoFrame when the
// access unit gave no picture yet. The client calls Submit and Close from
// one goroutine; a sink bound to a UI thread hands the call over itself.
type Sink interface {
	Submit(au []byte, pts int64) (player.Timing, error)
	Close() error
}

// SinkOpener opens the sink once the negotiated codec and size are known.
type SinkOpener func(decode.Config) (Sink, error)

var _ Sink = (*player.Player)(nil)

// decodeSink decodes each access unit and gives the picture straight back,
// for headless benchmarks. A decoder that returns before the GPU is done
// is waited for, so that decode and end-to-end times include the GPU's.
type decodeSink struct {
	dec   decode.Decoder
	after func()
}

func (s *decodeSink) Submit(au []byte, pts int64) (player.Timing, error) {
	f, err := s.dec.Decode(au, pts)
	if err != nil {
		return player.Timing{PTS: pts}, err
	}
	if fin, ok := s.dec.(decode.Finisher); ok {
		fin.Finish(&f)
	}
	s.dec.Release(f)
	return player.Timing{PTS: f.PTS, Decode: f.Latency}, nil
}

func (s *decodeSink) Close() error {
	err := s.dec.Close()
	if s.after != nil {
		s.after()
	}
	return err
}

// NullSink accepts every access unit without decoding it.
type NullSink struct {
	Units atomic.Uint64
	Bytes atomic.Uint64
	// OnUnit, when set, sees every access unit; au is valid only during the
	// call.
	OnUnit func(au []byte, pts int64)
}

func (s *NullSink) Submit(au []byte, pts int64) (player.Timing, error) {
	if len(au) == 0 {
		return player.Timing{PTS: pts}, errors.New("session: empty access unit")
	}
	s.Units.Add(1)
	s.Bytes.Add(uint64(len(au)))
	if s.OnUnit != nil {
		s.OnUnit(au, pts)
	}
	return player.Timing{PTS: pts}, nil
}

func (s *NullSink) Close() error { return nil }

// NullSinkOpener returns s for every session.
func NullSinkOpener(s *NullSink) SinkOpener {
	return func(decode.Config) (Sink, error) { return s, nil }
}
