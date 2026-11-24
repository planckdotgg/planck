package audio

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"
)

func TestPickVirtualMic(t *testing.T) {
	names := []string{"1 - M156F1 (AMD High Definition Audio Device)", "Speakers (Steam Streaming Speakers)", "CABLE Input (VB-Audio Virtual Cable)", "Speakers (Steam Streaming Microphone)"}
	if i := pickVirtualMic(names, ""); i != 3 {
		t.Fatalf("auto pick %d, want Steam Streaming Microphone (3); Steam Streaming Speakers is not a mic", i)
	}
	if i := pickVirtualMic(names, "cable"); i != 2 {
		t.Fatalf("pick \"cable\" = %d, want 2", i)
	}
	if i := pickVirtualMic(names[:2], ""); i != -1 {
		t.Fatalf("no virtual mic among %q, got %d", names[:2], i)
	}
	if i := pickVirtualMic([]string{"MacBook Pro Speakers", "BlackHole 2ch"}, ""); i != 1 {
		t.Fatalf("BlackHole pick %d", i)
	}
	if i := pickVirtualMic(names, "nothing like it"); i != -1 {
		t.Fatalf("unmatched want picked %d", i)
	}
}

func TestPairedInput(t *testing.T) {
	inputs := []string{"Microphone (Parsec Virtual Audio)", "Microphone (Steam Streaming Microphone)", "CABLE Output (VB-Audio Virtual Cable)"}
	for out, want := range map[string]int{
		"Speakers (Steam Streaming Microphone)": 1,
		"CABLE Input (VB-Audio Virtual Cable)":  2,
		"Speakers (Steam Streaming Speakers)":   -1,
		"BlackHole 2ch":                         -1,
	} {
		if got := pairedInput(out, inputs); got != want {
			t.Errorf("pairedInput(%q) = %d, want %d", out, got, want)
		}
	}
}

// Planck Virtual Audio wins over every third-party virtual mic, whatever
// order Windows lists the endpoints in, and the others still work without it.
func TestPickVirtualMicPrefersPlanck(t *testing.T) {
	names := []string{
		"Speakers (Steam Streaming Microphone)",
		"CABLE Input (VB-Audio Virtual Cable)",
		"Speakers (Realtek(R) Audio)",
		"Speakers (Planck Virtual Audio)",
		"BlackHole 2ch",
	}
	if i := pickVirtualMic(names, ""); i != 3 {
		t.Fatalf("auto pick %d (%q), want Planck Virtual Audio (3)", i, names[max(i, 0)])
	}
	if i := pickVirtualMic(names, "planck"); i != 3 {
		t.Fatalf("pick \"planck\" = %d, want 3", i)
	}
	if i := pickVirtualMic(names, "steam"); i != 0 {
		t.Fatalf("an explicit want still beats Planck: pick \"steam\" = %d, want 0", i)
	}
	without := []string{names[0], names[1], names[2], names[4]}
	if i := pickVirtualMic(without, ""); i != 0 {
		t.Fatalf("without Planck the pick is %d, want Steam Streaming Microphone (0)", i)
	}
	if !strings.Contains(VirtualMicHint, "Planck Virtual Audio") || strings.Index(VirtualMicHint, "Planck") > strings.Index(VirtualMicHint, "Steam") {
		t.Fatalf("hint %q should name Planck Virtual Audio first", VirtualMicHint)
	}
}

// Windows names a second instance "2- Planck Virtual Audio" on both sides;
// the pairing must still find the matching microphone.
func TestPairedInputPlanck(t *testing.T) {
	inputs := []string{"Microphone (Steam Streaming Microphone)", "Microphone (Planck Virtual Audio)", "Microphone (2- Planck Virtual Audio)"}
	for out, want := range map[string]int{
		"Speakers (Planck Virtual Audio)":    1,
		"Speakers (2- Planck Virtual Audio)": 2,
	} {
		if got := pairedInput(out, inputs); got != want {
			t.Errorf("pairedInput(%q) = %d, want %d", out, got, want)
		}
	}
}

// Only Steam Streaming Microphone needs its two sides to match: Planck
// Virtual Audio converts mono and stereo, so a mono speaker side feeding a
// stereo microphone side is fine.
func TestPassthroughFormatErr(t *testing.T) {
	for _, tc := range []struct {
		output, input                string
		outCh, outRate, inCh, inRate int
		setup                        bool
	}{
		{"Speakers (Planck Virtual Audio)", "Microphone (Planck Virtual Audio)", 1, 48000, 2, 48000, false},
		{"Speakers (Planck Virtual Audio)", "Microphone (Planck Virtual Audio)", 2, 48000, 1, 48000, false},
		{"CABLE Input (VB-Audio Virtual Cable)", "CABLE Output (VB-Audio Virtual Cable)", 2, 48000, 2, 44100, false},
		{"Speakers (Steam Streaming Microphone)", "Microphone (Steam Streaming Microphone)", 2, 48000, 2, 48000, false},
		{"Speakers (Steam Streaming Microphone)", "Microphone (Steam Streaming Microphone)", 2, 48000, 1, 48000, true},
		{"Speakers (Steam Streaming Microphone)", "Microphone (Steam Streaming Microphone)", 2, 48000, 2, 44100, true},
	} {
		err := passthroughFormatErr(tc.output, tc.input, tc.outCh, tc.outRate, tc.inCh, tc.inRate)
		if got := errors.Is(err, ErrVirtualMicSetup); got != tc.setup {
			t.Errorf("%s %dch/%d -> %dch/%d: err %v, want setup error %v", tc.output, tc.outCh, tc.outRate, tc.inCh, tc.inRate, err, tc.setup)
		}
	}
}

// The mic path end to end without devices: a 5 ms PCM stream and a 10 ms
// mono Opus VoIP stream, each with one packet lost, through the jitter
// buffer and play-out.
func TestMicPlayoutPCMAndOpus(t *testing.T) {
	for _, tc := range []struct {
		name  string
		frame time.Duration
		opus  bool
	}{{"pcm5ms", MicPCMFrameDuration, false}, {"opus10ms", 10 * time.Millisecond, true}} {
		t.Run(tc.name, func(t *testing.T) {
			n := FrameSamples(tc.frame)
			var enc *Encoder
			var dec FrameDecoder = NewPCMDecoder(tc.frame, MicChannels)
			if tc.opus {
				var err error
				if enc, err = NewEncoder(EncoderConfig{Channels: MicChannels, Voice: true, Bitrate: 32000}); err != nil {
					t.Fatal(err)
				}
				if dec, err = NewDecoderChannels(tc.frame, MicChannels); err != nil {
					t.Fatal(err)
				}
			}
			jb := NewJitterBuffer(JitterConfig{FrameDuration: tc.frame, TargetDepth: 2 * tc.frame})
			play := NewPlayout(jb, dec)
			in := make([]int16, n)
			pkt := make([]byte, MaxPacketSize)
			out := make([]int16, n)
			var got [3]int
			var energy float64
			for seq := range 60 {
				for i := range in {
					in[i] = int16(8000 * math.Sin(2*math.Pi*440*float64(seq*n+i)/SampleRate))
				}
				size := PutPCM16(pkt, in)
				if enc != nil {
					var err error
					if size, err = enc.Encode(in, pkt); err != nil {
						t.Fatal(err)
					}
				}
				if seq != 30 {
					jb.Push(uint16(seq), pkt[:size])
				}
				st, err := play.Next(out)
				if err != nil {
					t.Fatal(err)
				}
				got[st]++
				if seq > 40 {
					for _, v := range out {
						energy += float64(v) * float64(v)
					}
				}
			}
			if got[Missing] != 1 || jb.Stats().Lost != 1 {
				t.Fatalf("statuses %v, jitter %+v: want exactly the lost packet concealed", got, jb.Stats())
			}
			if rms := math.Sqrt(energy / float64(19*n)); rms < 2000 {
				t.Fatalf("play-out after the loss has RMS %.0f; the tone did not come back", rms)
			}
		})
	}
}

func TestPCMDecoderRejectsShortPacket(t *testing.T) {
	d := NewPCMDecoder(MicPCMFrameDuration, MicChannels)
	if _, err := d.Decode(make([]byte, 100), make([]int16, d.FrameSamples())); err == nil {
		t.Fatal("100-byte packet decoded")
	}
}
