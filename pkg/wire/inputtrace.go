package wire

// FeatureInputTrace, from a host, says it reads InputTrace.
const FeatureInputTrace uint32 = 1 << 18

// MsgInputTrace goes client to host, only to a host whose Hello carries
// FeatureInputTrace: once as the session starts and whenever the client's
// input trace turns on or off.
const MsgInputTrace MsgType = 24

// InputTrace asks the host to trace its input and cursor while the
// client traces its own (pkg/inputtrace), or to stop.
type InputTrace struct {
	On bool
}

const inputTraceOn = 1 << 0

func (*InputTrace) Type() MsgType { return MsgInputTrace }

func (m *InputTrace) appendBody(b []byte) []byte {
	var f byte
	if m.On {
		f |= inputTraceOn
	}
	return append(b, f)
}

// decodeBody ignores flag bits it does not know, which a later client may
// set.
func (m *InputTrace) decodeBody(d *decoder) { m.On = d.u8()&inputTraceOn != 0 }
