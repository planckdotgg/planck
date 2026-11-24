package audio

import (
	"math"
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
