package wire

import "encoding/binary"

// DisplayPhase is the client display's refresh and how long before a
// refresh its last pictures were ready (pkg/phaselock Report): the host
// steers the phase of its frames with it.
type DisplayPhase struct {
	Valid, Locked bool
	// Direct and Composited say how the client's system shows the
	// pictures; neither is set while it cannot tell.
	Direct, Composited bool
	PeriodNs           uint32
	// ErrorNs is the median slack minus the target, wrapped into half a
	// refresh either way: above 0 the pictures were ready early.
	ErrorNs                     int32
	SlackNs, TargetNs, JitterNs uint32
	// LatePermille and CompositedPermille are the shares of pictures that
	// missed their refresh and that a compositor showed a refresh or more
	// later, in thousandths.
	LatePermille, CompositedPermille uint16
	// Oldest and Newest are the video frames measured.
	Oldest, Newest uint32
}

const (
	phaseValid = 1 << iota
	phaseLocked
	phaseDirect
	phaseComposited
)

func (*DisplayPhase) Type() MsgType { return MsgDisplayPhase }

func (m *DisplayPhase) appendBody(b []byte) []byte {
	var f byte
	for i, on := range []bool{m.Valid, m.Locked, m.Direct, m.Composited} {
		if on {
			f |= 1 << i
		}
	}
	b = append(b, f)
	b = binary.BigEndian.AppendUint32(b, m.PeriodNs)
	b = binary.BigEndian.AppendUint32(b, uint32(m.ErrorNs))
	b = binary.BigEndian.AppendUint32(b, m.SlackNs)
	b = binary.BigEndian.AppendUint32(b, m.TargetNs)
	b = binary.BigEndian.AppendUint32(b, m.JitterNs)
	b = binary.BigEndian.AppendUint16(b, m.LatePermille)
	b = binary.BigEndian.AppendUint16(b, m.CompositedPermille)
	b = binary.BigEndian.AppendUint32(b, m.Oldest)
	return binary.BigEndian.AppendUint32(b, m.Newest)
}

// decodeBody ignores flag bits it does not know, which a later client may
// set.
func (m *DisplayPhase) decodeBody(d *decoder) {
	f := d.u8()
	m.Valid, m.Locked = f&phaseValid != 0, f&phaseLocked != 0
	m.Direct, m.Composited = f&phaseDirect != 0, f&phaseComposited != 0
	m.PeriodNs = d.u32()
	m.ErrorNs = int32(d.u32())
	m.SlackNs = d.u32()
	m.TargetNs = d.u32()
	m.JitterNs = d.u32()
	m.LatePermille = d.u16()
	m.CompositedPermille = d.u16()
	m.Oldest = d.u32()
	m.Newest = d.u32()
}
