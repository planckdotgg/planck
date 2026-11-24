package wire

import (
	"bytes"
	"errors"
	"io"
	"reflect"
	"testing"
)

func TestPeerStateRoundTrip(t *testing.T) {
	for _, want := range []*PeerState{
		{},
		{ClipboardSend: true, ClipboardRecv: true, MicDevice: "Speakers (Steam Streaming Microphone)"},
		{ClipboardRecv: true, MicHint: "Virtual mic not detected"},
	} {
		var buf bytes.Buffer
		if err := WriteMessage(&buf, want); err != nil {
			t.Fatal(err)
		}
		got, err := ReadMessage(&buf)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %+v, want %+v", got, want)
		}
	}
}

func TestAudioHeaderFlowAndLegacyZeros(t *testing.T) {
	b := make([]byte, AudioHeaderLen+3)
	h := AudioHeader{Flow: FlowMic, Codec: AudioPCM16, FrameQuarterMs: 20, Seq: 7, Timestamp: -5}
	h.Put(b)
	copy(b[AudioHeaderLen:], "abc")
	got, payload, err := ParseAudio(b)
	if err != nil || got != h || string(payload) != "abc" {
		t.Fatalf("got %+v %q %v, want %+v", got, payload, err, h)
	}
	// A host from before the mic flow sends zeros in bytes 1-3.
	legacy := []byte{KindAudio, 0, 0, 0, 0, 0, 0, 9, 0, 0, 0, 0, 0, 0, 0, 1}
	got, _, err = ParseAudio(legacy)
	if err != nil || got.Flow != FlowDesktop || got.Codec != AudioOpus || got.FrameQuarterMs != 0 || got.Seq != 9 {
		t.Fatalf("legacy header parsed as %+v, %v", got, err)
	}
}

func TestClipRoundTripAndLimits(t *testing.T) {
	origin := [16]byte{1, 2, 3}
	var stream bytes.Buffer
	text := NewClip(ClipText, origin, []byte("héllo"))
	png := NewClip(ClipPNG, origin, bytes.Repeat([]byte{0x89}, 4096))
	big := NewClip(ClipText, origin, bytes.Repeat([]byte{'x'}, 2000))
	tampered := NewClip(ClipText, origin, []byte("one"))
	tampered.Data = []byte("two")
	after := NewClip(ClipText, origin, []byte("after"))
	for _, c := range []*Clip{text, png, big, tampered, after} {
		if err := WriteClip(&stream, c); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []*Clip{text, png} {
		got, err := ReadClip(&stream, 1024*1024)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("got %+v %v, want %+v", got, err, want)
		}
	}
	if _, err := ReadClip(&stream, 1000); !errors.Is(err, ErrClipTooLarge) {
		t.Fatalf("2000-byte item with a 1000-byte limit: %v, want ErrClipTooLarge", err)
	}
	if _, err := ReadClip(&stream, 0); !errors.Is(err, ErrClipHash) {
		t.Fatalf("tampered item: %v, want ErrClipHash", err)
	}
	if got, err := ReadClip(&stream, 0); err != nil || string(got.Data) != "after" {
		t.Fatalf("stream after refused items: %+v %v", got, err)
	}
	if _, err := ReadClip(&stream, 0); err != io.EOF {
		t.Fatalf("end: %v", err)
	}
}

func TestWriteClipRefusesOversizeAndUnknownFormat(t *testing.T) {
	var w bytes.Buffer
	over := &Clip{Format: ClipPNG, Data: make([]byte, MaxClip+1)}
	if err := WriteClip(&w, over); !errors.Is(err, ErrClipTooLarge) {
		t.Fatalf("oversize: %v", err)
	}
	if err := WriteClip(&w, &Clip{Format: 9}); !errors.Is(err, ErrClipFormat) {
		t.Fatalf("format 9: %v", err)
	}
	if w.Len() != 0 {
		t.Fatalf("refused items wrote %d bytes", w.Len())
	}
}
