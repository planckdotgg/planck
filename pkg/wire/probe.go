package wire

import (
	"encoding/binary"
	"errors"
)

// FeatureCalibration, from a host, says it serves calibration: it answers
// ProbeRequest with a train of probe datagrams, and streams its synthetic
// pattern, not its screen, to a client whose capabilities ask for a
// calibration stream. A client sends ProbeRequest only to such a host.
const FeatureCalibration uint32 = 1 << 17

const (
	// MsgProbeRequest goes client to host, only to a host whose Hello
	// carries FeatureCalibration.
	MsgProbeRequest MsgType = 22
	// MsgProbeDone goes host to client once a requested train went out.
	MsgProbeDone MsgType = 23
)

// MaxProbeCount bounds the datagrams of one probe train.
const MaxProbeCount = 1 << 16

// ProbeRequest asks the host for a train of Count probe datagrams of Size
// bytes of plaintext each, paced at RateKbps, or back to back at 0.
type ProbeRequest struct {
	Train    uint32
	Count    uint32
	Size     uint16
	RateKbps uint32
}

// ProbeDone says the host sent Sent datagrams of train Train, the first to
// the last SendNs apart; Sent 0 is a refused request.
type ProbeDone struct {
	Train  uint32
	Sent   uint32
	SendNs uint64
}

func (*ProbeRequest) Type() MsgType { return MsgProbeRequest }
func (*ProbeDone) Type() MsgType    { return MsgProbeDone }

func (m *ProbeRequest) appendBody(b []byte) []byte {
	b = binary.BigEndian.AppendUint32(b, m.Train)
	b = binary.BigEndian.AppendUint32(b, m.Count)
	b = binary.BigEndian.AppendUint16(b, m.Size)
	return binary.BigEndian.AppendUint32(b, m.RateKbps)
}

func (m *ProbeRequest) decodeBody(d *decoder) {
	m.Train = d.u32()
	m.Count = d.u32()
	m.Size = d.u16()
	m.RateKbps = d.u32()
}

func (m *ProbeDone) appendBody(b []byte) []byte {
	b = binary.BigEndian.AppendUint32(b, m.Train)
	b = binary.BigEndian.AppendUint32(b, m.Sent)
	return binary.BigEndian.AppendUint64(b, m.SendNs)
}

func (m *ProbeDone) decodeBody(d *decoder) {
	m.Train = d.u32()
	m.Sent = d.u32()
	m.SendNs = d.u64()
}

// KindProbe is a probe datagram of a calibration train, host to client.
const KindProbe byte = 0x18

// ProbeHeaderLen is the plaintext header of a probe datagram; padding
// fills the rest of it to the requested size.
const ProbeHeaderLen = 24

// ProbeHeader is datagram Index of the Count of train Train, sent at the
// host's monotonic SentTS.
type ProbeHeader struct {
	Train, Index, Count uint32
	SentTS              int64
}

var errBadProbe = errors.New("wire: bad probe datagram")

func (h *ProbeHeader) Put(b []byte) {
	_ = b[ProbeHeaderLen-1]
	b[0], b[1], b[2], b[3] = KindProbe, 0, 0, 0
	binary.BigEndian.PutUint32(b[4:], h.Train)
	binary.BigEndian.PutUint32(b[8:], h.Index)
	binary.BigEndian.PutUint32(b[12:], h.Count)
	binary.BigEndian.PutUint64(b[16:], uint64(h.SentTS))
}

func (h *ProbeHeader) Parse(b []byte) error {
	if len(b) < ProbeHeaderLen || b[0] != KindProbe {
		return errBadProbe
	}
	h.Train = binary.BigEndian.Uint32(b[4:])
	h.Index = binary.BigEndian.Uint32(b[8:])
	h.Count = binary.BigEndian.Uint32(b[12:])
	h.SentTS = int64(binary.BigEndian.Uint64(b[16:]))
	if h.Count == 0 || h.Count > MaxProbeCount || h.Index >= h.Count {
		return errBadProbe
	}
	return nil
}
