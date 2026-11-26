package transport

import (
	"net"
	"net/netip"
)

// maxTrainBytes bounds one offloaded send: Windows refuses a UDP send
// offload buffer over 64 KiB.
const maxTrainBytes = 63 << 10

// trainWriter sends runs of equal-length datagrams to one peer with a
// single UDP send-offload call (Windows UDP_SEND_MSG_SIZE), which the
// stack or the NIC cuts into datagrams. A Windows WriteToUDP costs 4 to 6
// us, about 0.4 ms of sender time for an 80-datagram frame. Where the OS
// has no offload, or a train once failed with offload and went through
// one datagram at a time, add writes each datagram as it comes. Not safe
// for concurrent use; it does not allocate.
type trainWriter struct {
	conn *net.UDPConn
	gso  bool
	buf  []byte
	seg  int
	n    int
	oob  []byte
	// sendMsg is conn.WriteMsgUDPAddrPort.
	sendMsg func(b, oob []byte, addr netip.AddrPort) (int, int, error)
}

func newTrainWriter(c *net.UDPConn) trainWriter {
	w := trainWriter{conn: c}
	if c != nil && gsoSupported(c) {
		w.sendMsg = c.WriteMsgUDPAddrPort
		w.gso = true
		w.buf = make([]byte, 0, maxTrainBytes)
		w.oob = make([]byte, segmentOOBLen)
	}
	return w
}

// add queues sealed for peer, or sends it at once without offload. A
// datagram shorter than the train ends it, as offload allows only the
// last segment to be short. It returns the datagrams that went out.
func (w *trainWriter) add(sealed []byte, peer netip.AddrPort) (int, error) {
	if !w.gso {
		if _, err := w.conn.WriteToUDPAddrPort(sealed, peer); err != nil {
			return 0, err
		}
		return 1, nil
	}
	var sent int
	if w.n > 0 && (len(sealed) > w.seg || len(w.buf)+len(sealed) > cap(w.buf)) {
		n, err := w.flush(peer)
		if err != nil {
			return n, err
		}
		sent = n
	}
	if w.n == 0 {
		w.seg = len(sealed)
	}
	w.buf = append(w.buf, sealed...)
	w.n++
	if len(sealed) < w.seg {
		n, err := w.flush(peer)
		return sent + n, err
	}
	return sent, nil
}

// flush sends the queued train and returns the datagrams that went out.
func (w *trainWriter) flush(peer netip.AddrPort) (int, error) {
	n, buf, seg := w.n, w.buf, w.seg
	w.n, w.buf = 0, w.buf[:0]
	switch {
	case n == 0:
		return 0, nil
	case n == 1:
		if _, err := w.conn.WriteToUDPAddrPort(buf, peer); err != nil {
			return 0, err
		}
		return 1, nil
	}
	putSegmentOOB(w.oob, seg)
	if _, _, err := w.sendMsg(buf, w.oob, peer); err == nil {
		return n, nil
	}
	// Offload refused the train. If the same datagrams go out one at a
	// time, offload is what failed: stop using it rather than pay a failed
	// call a frame.
	sent := 0
	for off := 0; off < len(buf); off += seg {
		if _, err := w.conn.WriteToUDPAddrPort(buf[off:min(off+seg, len(buf))], peer); err != nil {
			return sent, err
		}
		sent++
	}
	w.gso = false
	return sent, nil
}
