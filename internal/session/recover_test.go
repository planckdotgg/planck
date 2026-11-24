package session

import (
	"errors"
	"testing"
	"time"

	"github.com/planckdotgg/planck/pkg/capture"
	"github.com/planckdotgg/planck/pkg/encode"
)

// lostSource loses its duplication once, as DXGI does when a full-screen
// game switches the display's mode, and recovers in place.
type lostSource struct {
	lost       bool
	recovers   int
	recoverErr error
}

func (s *lostSource) Next(time.Duration) (capture.Frame, error) {
	if s.lost {
		return capture.Frame{}, capture.ErrAccessLost
	}
	return capture.Frame{Width: 64, Height: 64}, nil
}

func (s *lostSource) Recover(time.Duration, time.Duration) error {
	s.recovers++
	if s.recoverErr != nil {
		return s.recoverErr
	}
	s.lost = false
	return nil
}

func (s *lostSource) Release() error { return nil }
func (s *lostSource) Close() error   { return nil }

type closedEncoder struct{ closed int }

func (e *closedEncoder) Caps() encode.Caps { return encode.Caps{} }
func (e *closedEncoder) Encode(capture.Frame, bool, []byte) (encode.Packet, error) {
	return encode.Packet{Data: []byte{1}}, nil
}
func (e *closedEncoder) SetBitrate(int) error { return nil }
func (e *closedEncoder) Close() error         { e.closed++; return nil }

// TestPipeRecoversLostCapture: a lost capture comes back in place, on the
// same pipe: the loop hears a timeout, not a failure, and the encoder,
// which may hold the old duplication's images, goes and opens again with
// a keyframe. A source that cannot recover, or fails to, reopens the pipe.
func TestPipeRecoversLostCapture(t *testing.T) {
	src := &lostSource{lost: true}
	enc := &closedEncoder{}
	p := &sourcePipe{src: src, enc: enc, w: 64, h: 64}
	if _, _, err := p.Next(time.Millisecond, false, nil); !errors.Is(err, capture.ErrTimeout) {
		t.Fatalf("lost capture: %v, want a timeout after recovering", err)
	}
	if src.recovers != 1 || enc.closed != 1 || p.enc != nil {
		t.Fatalf("%d recovers, encoder closed %d times, encoder kept %v", src.recovers, enc.closed, p.enc != nil)
	}

	failing := &lostSource{lost: true, recoverErr: errors.Join(capture.ErrAccessLost, errors.New("E_ACCESSDENIED"))}
	p = &sourcePipe{src: failing, enc: &closedEncoder{}}
	if _, _, err := p.Next(time.Millisecond, false, nil); !errors.Is(err, capture.ErrAccessLost) {
		t.Fatalf("failed recovery: %v, want ErrAccessLost", err)
	}

	p = &sourcePipe{src: &onlySource{lost: true}, enc: &closedEncoder{}}
	if _, _, err := p.Next(time.Millisecond, false, nil); !errors.Is(err, capture.ErrAccessLost) {
		t.Fatalf("source without Recover: %v, want ErrAccessLost", err)
	}
}

// onlySource is a capture.Source and nothing more.
type onlySource struct{ lost bool }

func (s *onlySource) Next(time.Duration) (capture.Frame, error) {
	return capture.Frame{}, capture.ErrAccessLost
}
func (s *onlySource) Release() error { return nil }
func (s *onlySource) Close() error   { return nil }
