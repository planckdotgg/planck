package wire

import (
	"bytes"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/planckdotgg/planck/pkg/defaults"
)

func allMessages() []Message {
	cfg := SessionConfig{
		Codec: CodecHEVC, Chroma: Chroma444, Width: 2560, Height: 1440, FPS: 240,
		BitrateKbps: 150000, Slices: 4, SubFrame: true, AudioSampleRate: 48000, AudioChannels: 2,
	}
	return []Message{
		&Hello{Version: Version, Role: RoleClient, Name: "mac", Features: 0xdeadbeef},
		&AuthRequest{Method: AuthPIN, DeviceName: "Chris's MacBook", PINProof: [32]byte{1, 2, 3, 31: 9}},
		&AuthResponse{Status: AuthOK, Identity: "watzon@github", PINProof: [32]byte{7, 31: 8}, Message: "welcome"},
		&cfg,
		&SessionReady{SessionID: 0x0102030405060708, MediaPort: 47998, ShardSize: 1091, Config: cfg},
		&PLI{},
		&RefInvalidate{FrameID: 123456},
		&StatsReport{RTTMicros: 300, JitterMicros: 50, LossPermille: 20, FramesReceived: 1000,
			FramesDropped: 2, FramesRecovered: 15, DecodeMicros: 900, ReceivedKbps: 98000},
		&Goodbye{Reason: GoodbyeNormal, Message: "bye"},
		&Capabilities{Role: RoleClient, JSON: []byte(`{"v":1,"client":{"os":"darwin"},"route":{"kind":1},"overrides":{"codec":"pyrowave"}}`)},
		&Profile{JSON: []byte(`{"route":1,"maxDatagram":1452,"codec":4}`)},
		&Resize{Width: 2560, Height: 1440},
		&HostStats{CaptureMicros: 2900, EncodeMicros: 1410},
	}
}

func TestControlRoundTrip(t *testing.T) {
	var stream bytes.Buffer
	for _, m := range allMessages() {
		if err := WriteMessage(&stream, m); err != nil {
			t.Fatalf("write %T: %v", m, err)
		}
	}
	for _, want := range allMessages() {
		got, err := ReadMessage(&stream)
		if err != nil {
			t.Fatalf("read %T: %v", want, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("round trip %T:\n got %+v\nwant %+v", want, got, want)
		}
	}
	if _, err := ReadMessage(&stream); err != io.EOF {
		t.Fatalf("after last message: %v, want EOF", err)
	}
}

var update = flag.Bool("update", false, "rewrite testdata/golden")

// TestControlGolden pins the encoding of every control message, so a
// change that would break an older peer fails here.
func TestControlGolden(t *testing.T) {
	for _, m := range allMessages() {
		name := fmt.Sprintf("%02d_%s", m.Type(), strings.ToLower(strings.TrimPrefix(fmt.Sprintf("%T", m), "*wire.")))
		t.Run(name, func(t *testing.T) {
			b, err := AppendMessage(nil, m)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join("testdata", "golden", name+".hex")
			if *update {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(hex.EncodeToString(b)+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if got := hex.EncodeToString(b); got != strings.TrimSpace(string(want)) {
				t.Fatalf("encoding changed:\n got %s\nwant %s", got, want)
			}
		})
	}
}

func TestCapabilitiesLimits(t *testing.T) {
	for _, m := range []Message{
		&Capabilities{Role: RoleHost, JSON: make([]byte, MaxCapsJSON+1)},
		&Profile{JSON: make([]byte, MaxCapsJSON+1)},
	} {
		if _, err := AppendMessage(nil, m); !errors.Is(err, ErrTooLarge) {
			t.Errorf("encode %T over the limit: %v, want ErrTooLarge", m, err)
		}
	}
	over := []byte{byte(MsgCapabilities), byte(RoleHost), 0, 0, 0xc0, 1}
	if _, err := DecodeMessage(over); !errors.Is(err, ErrTooLarge) {
		t.Errorf("decode Capabilities length %d: %v, want ErrTooLarge", MaxCapsJSON+1, err)
	}
	if _, err := DecodeMessage([]byte{byte(MsgProfile), 0xff, 0xff, 0xff, 0xff}); !errors.Is(err, ErrTooLarge) {
		t.Errorf("decode Profile length 4G: %v, want ErrTooLarge", err)
	}
	if _, err := DecodeMessage([]byte{byte(MsgProfile), 0, 0, 0, 5, '{', '}'}); !errors.Is(err, ErrMalformed) {
		t.Errorf("decode short Profile: %v, want ErrMalformed", err)
	}
	m, err := DecodeMessage([]byte{byte(MsgProfile), 0, 0, 0, 0})
	if err != nil || m.(*Profile).JSON != nil {
		t.Errorf("empty Profile: %#v %v", m, err)
	}
	if b, err := AppendMessage(nil, &Capabilities{Role: RoleHost, JSON: make([]byte, MaxCapsJSON)}); err != nil || len(b) != 4+1+1+4+MaxCapsJSON {
		t.Errorf("Capabilities at the limit: len %d err %v", len(b), err)
	}
}

// TestCodecValuesMatchDefaults: the host copies pkg/defaults codecs onto the
// wire by value.
func TestCodecValuesMatchDefaults(t *testing.T) {
	pairs := [][2]uint8{
		{uint8(CodecH264), uint8(defaults.CodecH264)},
		{uint8(CodecHEVC), uint8(defaults.CodecHEVC)},
		{uint8(CodecAV1), uint8(defaults.CodecAV1)},
		{uint8(CodecPyroWave), uint8(defaults.CodecPyroWave)},
	}
	for _, p := range pairs {
		if p[0] != p[1] {
			t.Errorf("wire codec %d != defaults codec %d", p[0], p[1])
		}
	}
}

// TestLegacyPeerCompatibility: an old peer ignores the FeatureCapabilities
// bit, and the messages it knows keep their type numbers and encodings
// (TestControlGolden pins the bytes).
func TestLegacyPeerCompatibility(t *testing.T) {
	if FeatureCapabilities&(FeatureLANDatagram|FeatureInputDedup) != 0 {
		t.Fatal("FeatureCapabilities reuses an existing bit")
	}
	if MsgGoodbye != 9 || MsgCapabilities != 10 || MsgProfile != 11 {
		t.Fatalf("message numbers moved: goodbye %d capabilities %d profile %d", MsgGoodbye, MsgCapabilities, MsgProfile)
	}
	old := &Hello{Version: Version, Role: RoleHost, Name: "old", Features: FeatureLANDatagram | FeatureInputDedup}
	b, _ := AppendMessage(nil, old)
	m, err := DecodeMessage(b[4:])
	if err != nil || m.(*Hello).Features&FeatureCapabilities != 0 {
		t.Fatalf("old Hello: %+v %v", m, err)
	}
}

func TestControlRejectsMalformed(t *testing.T) {
	good, _ := AppendMessage(nil, &RefInvalidate{FrameID: 1})
	body := good[4:]
	cases := map[string][]byte{
		"empty":     {},
		"unknown":   {0xee},
		"truncated": body[:len(body)-1],
		"trailing":  append(append([]byte{}, body...), 0),
		"long str":  {byte(MsgGoodbye), 1, 0xff, 0xff},
	}
	for name, b := range cases {
		if _, err := DecodeMessage(b); err == nil {
			t.Errorf("%s: decoded %x without error", name, b)
		}
	}
	huge := []byte{0x7f, 0xff, 0xff, 0xff}
	if _, err := ReadMessage(bytes.NewReader(huge)); !errors.Is(err, ErrTooLarge) {
		t.Errorf("oversized frame: %v, want ErrTooLarge", err)
	}
}

func FuzzDecodeMessage(f *testing.F) {
	for _, m := range allMessages() {
		b, _ := AppendMessage(nil, m)
		f.Add(b[4:])
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		m, err := DecodeMessage(body)
		if err != nil {
			return
		}
		again, err := AppendMessage(nil, m)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(again[4:], body) {
			t.Fatalf("re-encoding differs:\n in %x\nout %x", body, again[4:])
		}
	})
}

func TestVideoHeaderRoundTripAndValidation(t *testing.T) {
	h := VideoHeader{
		VideoInfo:  VideoInfo{FrameID: 42, Keyframe: true, CaptureTS: 1e9, EncodeTS: 1e9 + 2e6, SliceCount: 4},
		SliceIndex: 3, BlockIndex: 1, BlockCount: 2, ShardIndex: 5, DataShards: 4, ParityShards: 2,
		ShardLen: 100, BlockLen: 350,
	}
	pkt := make([]byte, VideoHeaderLen+100)
	h.Put(pkt)
	var got VideoHeader
	if err := got.Parse(pkt); err != nil {
		t.Fatal(err)
	}
	if got != h {
		t.Fatalf("got %+v want %+v", got, h)
	}
	bad := []func(*VideoHeader){
		func(h *VideoHeader) { h.SliceIndex = h.SliceCount },
		func(h *VideoHeader) { h.SliceCount = MaxSlices + 1; h.SliceIndex = 0 },
		func(h *VideoHeader) { h.BlockIndex = h.BlockCount },
		func(h *VideoHeader) { h.ShardIndex = h.DataShards + h.ParityShards },
		func(h *VideoHeader) { h.DataShards = 0 },
		func(h *VideoHeader) { h.BlockLen = 401 },
		func(h *VideoHeader) { h.BlockLen = 300 },
		func(h *VideoHeader) { h.ShardLen = 99 },
	}
	for i, mutate := range bad {
		m := h
		mutate(&m)
		m.Put(pkt)
		if err := got.Parse(pkt); err == nil {
			t.Errorf("mutation %d accepted: %+v", i, m)
		}
	}
}

func TestDatagramRoundTrips(t *testing.T) {
	buf := make([]byte, 64)

	inputs := []Input{
		{Type: InputKey, Seq: 1, Timestamp: 5, Code: 0x04, Mods: 0x02, Down: true},
		{Type: InputMouseRel, Seq: 2, Timestamp: 6, X: -12, Y: 40},
		{Type: InputMouseAbs, Seq: 3, Timestamp: 7, X: 65535, Y: 0},
		{Type: InputMouseButton, Seq: 4, Timestamp: 8, Button: 3, Down: true},
		{Type: InputScroll, Seq: 5, Timestamp: 9, Y: -120},
		{Type: InputGamepad, Seq: 6, Timestamp: 10, Pad: 1, Buttons: 0x8001, LT: 200, RT: 1,
			LX: -32768, LY: 32767, RX: 5, RY: -5},
	}
	for _, in := range inputs {
		var got Input
		if err := got.Parse(in.Put(buf)); err != nil || got != in {
			t.Fatalf("input %+v: got %+v err %v", in, got, err)
		}
	}

	r := Rumble{Pad: 2, Seq: 9, Low: 100, High: 65535, DurationMs: 250}
	var gr Rumble
	if err := gr.Parse(r.Put(buf)); err != nil || gr != r {
		t.Fatalf("rumble: got %+v err %v", gr, err)
	}

	p := Ping{Seq: 77, T0: -5}
	var gp Ping
	if err := gp.Parse(p.Put(buf)); err != nil || gp != p {
		t.Fatalf("ping: got %+v err %v", gp, err)
	}
	po := Pong{Seq: 77, T0: 1, T1: 2, T2: 3}
	var gpo Pong
	if err := gpo.Parse(po.Put(buf)); err != nil || gpo != po {
		t.Fatalf("pong: got %+v err %v", gpo, err)
	}

	fb := Feedback{FrameID: 10, ArrivalTS: 123456789, Bytes: 40000, HasFinal: true, FinalOK: false,
		FinalFrameID: 2, FinalRecv: 30, FinalSent: 33}
	var gfb Feedback
	if err := gfb.Parse(fb.Put(buf)); err != nil || gfb != fb {
		t.Fatalf("feedback: got %+v err %v", gfb, err)
	}

	ah := AudioHeader{Seq: 3, Timestamp: 99}
	pkt := make([]byte, AudioHeaderLen+3)
	ah.Put(pkt)
	copy(pkt[AudioHeaderLen:], "opu")
	gah, payload, err := ParseAudio(pkt)
	if err != nil || gah != ah || string(payload) != "opu" {
		t.Fatalf("audio: %+v %q %v", gah, payload, err)
	}

	if err := gp.Parse(fb.Put(buf)); err == nil {
		t.Fatal("ping parser accepted a feedback datagram")
	}
}

func TestMediaCodecDoesNotAllocate(t *testing.T) {
	buf := make([]byte, VideoHeaderLen+64)
	h := VideoHeader{VideoInfo: VideoInfo{FrameID: 1, SliceCount: 1}, BlockCount: 1, DataShards: 1, ShardLen: 64, BlockLen: 64}
	in := Input{Type: InputGamepad, LX: 3}
	var gh VideoHeader
	var gin Input
	allocs := testing.AllocsPerRun(1000, func() {
		h.Put(buf)
		_ = gh.Parse(buf)
		_ = gin.Parse(in.Put(buf))
	})
	if allocs != 0 {
		t.Fatalf("media header encode/decode allocates %.1f times", allocs)
	}
}
