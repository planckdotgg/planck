package audio

// concealFrames is how many consecutive frames Opus concealment may cover
// before the output fades to silence; past that, extrapolated audio sounds
// worse than a gap.
const concealFrames = 4

// FrameDecoder turns one packet into one frame of PCM, or conceals a lost
// one. *Decoder (Opus) and *PCMDecoder are FrameDecoders.
type FrameDecoder interface {
	FrameSamples() int
	Channels() int
	Decode(packet []byte, pcm []int16) (int, error)
	Conceal(pcm []int16) (int, error)
}

// Playout turns a JitterBuffer and a FrameDecoder into a steady PCM stream:
// one frame per Next call, decoded, concealed, or silent.
type Playout struct {
	jb        *JitterBuffer
	dec       FrameDecoder
	packet    [MaxPacketSize]byte
	concealed int
}

func NewPlayout(jb *JitterBuffer, dec FrameDecoder) *Playout {
	return &Playout{jb: jb, dec: dec}
}

// Next fills pcm (FrameSamples()*Channels() values) with the next frame
// and returns what the jitter buffer produced for it.
func (p *Playout) Next(pcm []int16) (PopStatus, error) {
	frame := pcm[:p.dec.FrameSamples()*p.dec.Channels()]
	n, st := p.jb.Pop(p.packet[:])
	switch st {
	case Packet:
		p.concealed = 0
		_, err := p.dec.Decode(p.packet[:n], frame)
		return st, err
	case Missing:
		p.concealed++
		if p.concealed > concealFrames+1 {
			clear(frame)
			return st, nil
		}
		if _, err := p.dec.Conceal(frame); err != nil {
			return st, err
		}
		if p.concealed == concealFrames+1 {
			fadeOut(frame, p.dec.Channels())
		}
		return st, nil
	default:
		clear(frame)
		return st, nil
	}
}

func fadeOut(pcm []int16, channels int) {
	frames := len(pcm) / channels
	for i := 0; i < frames; i++ {
		g := int32(frames - i)
		for c := 0; c < channels; c++ {
			pcm[i*channels+c] = int16(int32(pcm[i*channels+c]) * g / int32(frames))
		}
	}
}
