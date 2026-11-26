package wire

import (
	"errors"
	"testing"
)

func TestProbeMessagesRoundTrip(t *testing.T) {
	for _, want := range []Message{
		&ProbeRequest{Train: 7, Count: 512, Size: 1452, RateKbps: 900_000},
		&ProbeRequest{Train: 1, Count: 1, Size: ProbeHeaderLen},
		&ProbeDone{Train: 7, Sent: 512, SendNs: 2_300_000},
		&ProbeDone{Train: 9},
	} {
		b, err := AppendMessage(nil, want)
		if err != nil {
			t.Fatal(err)
		}
		got, err := DecodeMessage(b[4:])
		if err != nil {
			t.Fatalf("%+v: %v", want, err)
		}
		switch w := want.(type) {
		case *ProbeRequest:
			if *got.(*ProbeRequest) != *w {
				t.Errorf("got %+v, want %+v", got, w)
			}
		case *ProbeDone:
			if *got.(*ProbeDone) != *w {
				t.Errorf("got %+v, want %+v", got, w)
			}
		}
		if _, err := DecodeMessage(b[4 : len(b)-1]); !errors.Is(err, ErrMalformed) {
			t.Errorf("%T short body: %v, want ErrMalformed", want, err)
		}
	}
}

// An older host reads a ProbeRequest as an unknown type and ends the
// session, so a client sends one only to a host with FeatureCalibration:
// the bit and the message types are ones nothing else uses.
func TestProbeNeedsTheFeature(t *testing.T) {
	if MsgProbeRequest <= MsgDisplayPhase || MsgProbeDone <= MsgDisplayPhase {
		t.Fatalf("probe types %d, %d are taken by older messages", MsgProbeRequest, MsgProbeDone)
	}
	others := FeatureLANDatagram | FeatureInputDedup | FeatureCapabilities | FeatureResize | FeaturePeerState |
		FeatureMic | FeatureClipboard | FeatureHostStats | FeatureCursorState | FeatureStreamSwitch |
		FeatureResizeDisplay | FeatureCursorShape | FeatureSwitchDisplay | FeatureRefInvalidate |
		FeatureRefInvalidateThrough | FeatureDisplayPhase
	if FeatureCalibration&others != 0 {
		t.Fatalf("FeatureCalibration %#x overlaps %#x", FeatureCalibration, others)
	}
	for _, k := range []byte{KindVideo, KindAudio, KindInput, KindRumble, KindPing, KindPong, KindFeedback} {
		if k == KindProbe {
			t.Fatalf("KindProbe %#x is taken", k)
		}
	}
}

func TestProbeHeader(t *testing.T) {
	want := ProbeHeader{Train: 3, Index: 99, Count: 100, SentTS: -5}
	b := make([]byte, MaxLANDatagram)
	want.Put(b)
	var got ProbeHeader
	if err := got.Parse(b); err != nil || got != want {
		t.Fatalf("got %+v, %v", got, err)
	}
	for name, h := range map[string]ProbeHeader{
		"index past count": {Train: 1, Index: 4, Count: 4},
		"no count":         {Train: 1},
		"huge count":       {Train: 1, Count: MaxProbeCount + 1},
	} {
		h.Put(b)
		if got.Parse(b) == nil {
			t.Errorf("%s: parsed", name)
		}
	}
	want.Put(b)
	if got.Parse(b[:ProbeHeaderLen-1]) == nil {
		t.Error("short datagram parsed")
	}
	b[0] = KindVideo
	if got.Parse(b) == nil {
		t.Error("video datagram parsed as a probe")
	}
}
