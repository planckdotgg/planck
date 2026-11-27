package wire

import (
	"errors"
	"testing"
)

func TestInputTraceRoundTrip(t *testing.T) {
	for _, want := range []InputTrace{{}, {On: true}} {
		b, err := AppendMessage(nil, &want)
		if err != nil {
			t.Fatal(err)
		}
		if len(b) != 4+2 {
			t.Fatalf("%+v encodes to %d bytes", want, len(b))
		}
		got, err := DecodeMessage(b[4:])
		if err != nil || *got.(*InputTrace) != want {
			t.Fatalf("%+v: got %+v, %v", want, got, err)
		}
	}
}

// A later client's flag bits are ignored; a short or long body is refused.
func TestInputTraceDecode(t *testing.T) {
	got, err := DecodeMessage([]byte{byte(MsgInputTrace), 0xfe})
	if err != nil || got.(*InputTrace).On {
		t.Fatalf("unknown bits alone: %+v, %v", got, err)
	}
	got, err = DecodeMessage([]byte{byte(MsgInputTrace), 0xff})
	if err != nil || !got.(*InputTrace).On {
		t.Fatalf("on with unknown bits: %+v, %v", got, err)
	}
	if _, err := DecodeMessage([]byte{byte(MsgInputTrace)}); !errors.Is(err, ErrMalformed) {
		t.Fatalf("short body: %v", err)
	}
	if _, err := DecodeMessage([]byte{byte(MsgInputTrace), 1, 0}); !errors.Is(err, ErrMalformed) {
		t.Fatalf("long body: %v", err)
	}
}

// An older host reads InputTrace as an unknown type, so a client sends it
// only to a host whose Hello carries FeatureInputTrace: a type and a bit
// nothing else uses.
func TestInputTraceNeedsTheFeature(t *testing.T) {
	for _, m := range []MsgType{MsgHello, MsgAuthRequest, MsgAuthResponse, MsgSessionConfig, MsgSessionReady, MsgPLI,
		MsgRefInvalidate, MsgStatsReport, MsgGoodbye, MsgCapabilities, MsgProfile, MsgResize, MsgPeerState, MsgHostStats,
		MsgCursorState, MsgStream, MsgCursorShape, MsgSwitchDisplay, MsgDisplayPhase, MsgProbeRequest, MsgProbeDone} {
		if m == MsgInputTrace {
			t.Fatalf("MsgInputTrace %d is taken", m)
		}
	}
	others := FeatureLANDatagram | FeatureInputDedup | FeatureCapabilities | FeatureResize | FeaturePeerState |
		FeatureMic | FeatureClipboard | FeatureHostStats | FeatureCursorState | FeatureStreamSwitch |
		FeatureResizeDisplay | FeatureCursorShape | FeatureSwitchDisplay | FeatureRefInvalidate |
		FeatureRefInvalidateThrough | FeatureDisplayPhase | FeatureCalibration
	if FeatureInputTrace&others != 0 {
		t.Fatalf("FeatureInputTrace %#x overlaps %#x", FeatureInputTrace, others)
	}
}
