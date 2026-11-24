package decode

import (
	"errors"
	"testing"

	"github.com/planckdotgg/planck/pkg/encode"
)

type fakeDecoder struct{ t Target }

func (fakeDecoder) Decode([]byte, int64) (Frame, error) { return Frame{}, ErrNoFrame }
func (fakeDecoder) Release(Frame)                       {}
func (fakeDecoder) Close() error                        { return nil }

func TestOpenRegistered(t *testing.T) {
	if _, err := Open(Target{}, Config{Codec: encode.AV1}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unregistered codec: got %v, want ErrUnsupported", err)
	}
	Register(Backend{Name: "fake", Codec: encode.PyroWave, Open: func(t Target, cfg Config) (Decoder, error) {
		return fakeDecoder{t}, nil
	}})
	t.Cleanup(func() { backendsMu.Lock(); delete(backends, encode.PyroWave); backendsMu.Unlock() })
	d, err := Open(Target{Device: 7, Queue: 9}, Config{Codec: encode.PyroWave, ChunkLen: 1391})
	if err != nil || d.(fakeDecoder).t.Queue != 9 {
		t.Fatalf("got %v, %v", d, err)
	}
}
