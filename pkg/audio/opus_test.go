package audio

import (
	"math"
	"testing"
	"time"
)

func sineFrame(dst []int16, start int, hzL, hzR float64) {
	for i := 0; i < len(dst)/Channels; i++ {
		t := float64(start+i) / SampleRate
		dst[2*i] = int16(12000 * math.Sin(2*math.Pi*hzL*t))
		dst[2*i+1] = int16(12000 * math.Sin(2*math.Pi*hzR*t))
	}
}

// bestSNR aligns got against want over the codec delay and returns the best
// signal-to-noise ratio in dB for one channel.
func bestSNR(want, got []int16, ch int) float64 {
	best := math.Inf(-1)
	skip := 4800 * Channels
	for delay := 0; delay < 1000; delay++ {
		var sig, noise float64
		for i := skip + ch; i+delay*Channels < len(got); i += Channels {
			w := float64(want[i])
			d := w - float64(got[i+delay*Channels])
			sig += w * w
			noise += d * d
		}
		if snr := 10 * math.Log10(sig/noise); snr > best {
			best = snr
		}
	}
	return best
}

func TestOpusRoundTripSine(t *testing.T) {
	for _, fd := range []time.Duration{10 * time.Millisecond, 20 * time.Millisecond} {
		t.Run(fd.String(), func(t *testing.T) {
			enc, err := NewEncoder(EncoderConfig{FrameDuration: fd})
			if err != nil {
				t.Fatal(err)
			}
			dec, err := NewDecoder(fd)
			if err != nil {
				t.Fatal(err)
			}
			n := enc.FrameSamples()
			if n != FrameSamples(fd) {
				t.Fatalf("frame samples %d, want %d", n, FrameSamples(fd))
			}
			pcm := make([]int16, n*Channels)
			out := make([]int16, n*Channels)
			pkt := make([]byte, MaxPacketSize)
			var want, got []int16
			var bytes int
			frames := int(time.Second / fd)
			for f := 0; f < frames; f++ {
				sineFrame(pcm, f*n, 1000, 440)
				size, err := enc.Encode(pcm, pkt)
				if err != nil {
					t.Fatal(err)
				}
				bytes += size
				k, err := dec.Decode(pkt[:size], out)
				if err != nil {
					t.Fatal(err)
				}
				if k != n {
					t.Fatalf("decoded %d samples, want %d", k, n)
				}
				want = append(want, pcm...)
				got = append(got, out...)
			}
			for ch := 0; ch < Channels; ch++ {
				if snr := bestSNR(want, got, ch); snr < 25 {
					t.Errorf("channel %d SNR %.1f dB, want >= 25", ch, snr)
				} else {
					t.Logf("channel %d SNR %.1f dB", ch, snr)
				}
			}
			kbps := float64(bytes*8) / 1000
			if kbps < 64 || kbps > 192 {
				t.Errorf("bitrate %.0f kbps, want about 128", kbps)
			}
		})
	}
}

func TestOpusConcealFillsFrame(t *testing.T) {
	enc, _ := NewEncoder(EncoderConfig{})
	dec, _ := NewDecoder(0)
	n := enc.FrameSamples()
	pcm := make([]int16, n*Channels)
	out := make([]int16, n*Channels)
	pkt := make([]byte, MaxPacketSize)
	for f := 0; f < 20; f++ {
		sineFrame(pcm, f*n, 1000, 1000)
		size, _ := enc.Encode(pcm, pkt)
		if _, err := dec.Decode(pkt[:size], out); err != nil {
			t.Fatal(err)
		}
	}
	k, err := dec.Conceal(out)
	if err != nil || k != n {
		t.Fatalf("conceal = %d, %v; want %d", k, err, n)
	}
	var energy float64
	for _, v := range out {
		energy += float64(v) * float64(v)
	}
	if energy == 0 {
		t.Error("concealment right after a tone is silent; want extrapolated audio")
	}
}

func TestOpusRejectsBadInput(t *testing.T) {
	if _, err := NewEncoder(EncoderConfig{FrameDuration: 5 * time.Millisecond}); err == nil {
		t.Error("5ms frames accepted")
	}
	enc, _ := NewEncoder(EncoderConfig{})
	if _, err := enc.Encode(make([]int16, 10), make([]byte, MaxPacketSize)); err == nil {
		t.Error("short frame accepted")
	}
	dec, _ := NewDecoder(0)
	if _, err := dec.Decode(nil, make([]int16, 960)); err == nil {
		t.Error("empty packet accepted by Decode")
	}
}

func TestOpusSteadyStateAllocs(t *testing.T) {
	enc, _ := NewEncoder(EncoderConfig{})
	dec, _ := NewDecoder(0)
	n := enc.FrameSamples()
	pcm := make([]int16, n*Channels)
	out := make([]int16, n*Channels)
	pkt := make([]byte, MaxPacketSize)
	f := 0
	step := func() {
		sineFrame(pcm, f*n, 1000, 440)
		f++
		size, err := enc.Encode(pcm, pkt)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := dec.Decode(pkt[:size], out); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 50; i++ {
		step()
	}
	if a := testing.AllocsPerRun(200, step); a != 0 {
		t.Errorf("encode+decode allocates %.1f times per frame, want 0", a)
	}
}
