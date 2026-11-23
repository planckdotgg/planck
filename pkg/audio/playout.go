package audio

// concealFrames is how many consecutive frames Opus concealment may cover
// before the output fades to silence; past that, extrapolated audio sounds
// worse than a gap.
const concealFrames = 4

// Playout turns a JitterBuffer and a Decoder into a steady PCM stream: one
// frame per Next call, decoded, concealed, or silent.
type Playout struct {
	jb        *JitterBuffer
	dec       *Decoder
	packet    [MaxPacketSize]byte
	concealed int
}

func NewPlayout(jb *JitterBuffer, dec *Decoder) *Playout {
	return &Playout{jb: jb, dec: dec}
}

// Next fills pcm (FrameSamples()*Channels values) with the next frame and
// returns what the jitter buffer produced for it.
func (p *Playout) Next(pcm []int16) (PopStatus, error) {
	frame := pcm[:p.dec.FrameSamples()*Channels]
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
			fadeOut(frame)
		}
		return st, nil
	default:
		clear(frame)
		return st, nil
	}
}

func fadeOut(pcm []int16) {
	frames := len(pcm) / Channels
	for i := 0; i < frames; i++ {
		g := int32(frames - i)
		for c := 0; c < Channels; c++ {
			pcm[i*Channels+c] = int16(int32(pcm[i*Channels+c]) * g / int32(frames))
		}
	}
}
