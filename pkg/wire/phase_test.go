package wire

import (
	"errors"
	"testing"
)

func TestDisplayPhaseRoundTrip(t *testing.T) {
	for _, want := range []DisplayPhase{
		{},
		{Valid: true},
		{Valid: true, Composited: true, ErrorNs: -4166666, PeriodNs: 8333333, CompositedPermille: 1000},
		{Valid: true, Locked: true, Direct: true, ErrorNs: 4166665, SlackNs: 9000000, TargetNs: 1200000, JitterNs: 1, LatePermille: 3, Oldest: 0xfffffff0, Newest: 15},
	} {
		b, err := AppendMessage(nil, &want)
		if err != nil {
			t.Fatal(err)
		}
		got, err := DecodeMessage(b[4:])
		if err != nil || *got.(*DisplayPhase) != want {
			t.Errorf("%+v: got %+v, %v", want, got, err)
		}
	}
}

// A later client's flag bits are ignored; a short or long body is refused.
func TestDisplayPhaseDecode(t *testing.T) {
	b, err := AppendMessage(nil, &DisplayPhase{Valid: true, PeriodNs: 8333333})
	if err != nil {
		t.Fatal(err)
	}
	body := append([]byte(nil), b[4:]...)
	body[1] |= 0xf0
	got, err := DecodeMessage(body)
	if err != nil || *got.(*DisplayPhase) != (DisplayPhase{Valid: true, PeriodNs: 8333333}) {
		t.Fatalf("unknown flag bits: got %+v, %v", got, err)
	}
	if _, err := DecodeMessage(body[:len(body)-1]); !errors.Is(err, ErrMalformed) {
		t.Fatalf("short body: %v, want ErrMalformed", err)
	}
	if _, err := DecodeMessage(append(body, 0)); !errors.Is(err, ErrMalformed) {
		t.Fatalf("long body: %v, want ErrMalformed", err)
	}
}

// An older host has no DisplayPhase: it would read one as an unknown type
// and end the session, so a client sends it only to a host whose Hello
// carries FeatureDisplayPhase. The bit is one no other feature uses.
func TestDisplayPhaseNeedsTheFeature(t *testing.T) {
	b, err := AppendMessage(nil, &DisplayPhase{Valid: true})
	if err != nil {
		t.Fatal(err)
	}
	if MsgType(b[4]) <= MsgSwitchDisplay {
		t.Fatalf("type %d is taken by an older message", b[4])
	}
	others := FeatureLANDatagram | FeatureInputDedup | FeatureCapabilities | FeatureResize | FeaturePeerState |
		FeatureMic | FeatureClipboard | FeatureHostStats | FeatureCursorState | FeatureStreamSwitch |
		FeatureResizeDisplay | FeatureCursorShape | FeatureSwitchDisplay | FeatureRefInvalidate | FeatureRefInvalidateThrough
	if FeatureDisplayPhase&others != 0 {
		t.Fatalf("FeatureDisplayPhase %#x overlaps %#x", FeatureDisplayPhase, others)
	}
}

// The feedback datagram an older host parses keeps its size and layout.
func TestFeedbackUnchanged(t *testing.T) {
	f := Feedback{FrameID: 7, ArrivalTS: 123456789, Bytes: 4096, HasFinal: true, FinalOK: true, FinalFrameID: 6, FinalRecv: 80, FinalSent: 84}
	b := f.Put(make([]byte, FeedbackLen))
	if len(b) != 28 {
		t.Fatalf("feedback is %d bytes, an older host takes 28", len(b))
	}
	var got Feedback
	if err := got.Parse(b); err != nil || got != f {
		t.Fatalf("got %+v, %v", got, err)
	}
}
