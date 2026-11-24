package transport

// seqWindow remembers which of the last seqWindowLen input Seqs arrived.
type seqWindow struct {
	top  uint32
	have bool
	bits [seqWindowLen / 64]uint64
}

const seqWindowLen = 1024

// seen records seq and reports whether it arrived before or is too old to
// tell. Seq 0 is never a duplicate: senders that do not number input use
// it.
func (w *seqWindow) seen(seq uint32) bool {
	if seq == 0 {
		return false
	}
	if !w.have {
		w.have, w.top = true, seq
		w.set(seq)
		return false
	}
	d := int32(seq - w.top)
	if d > 0 {
		if d >= seqWindowLen {
			w.bits = [seqWindowLen / 64]uint64{}
		} else {
			for i := w.top + 1; i != seq; i++ {
				w.clear(i)
			}
		}
		w.top = seq
		w.set(seq)
		return false
	}
	if -d >= seqWindowLen {
		return true
	}
	if w.get(seq) {
		return true
	}
	w.set(seq)
	return false
}

func (w *seqWindow) set(seq uint32)      { w.bits[seq%seqWindowLen/64] |= 1 << (seq % 64) }
func (w *seqWindow) clear(seq uint32)    { w.bits[seq%seqWindowLen/64] &^= 1 << (seq % 64) }
func (w *seqWindow) get(seq uint32) bool { return w.bits[seq%seqWindowLen/64]&(1<<(seq%64)) != 0 }
