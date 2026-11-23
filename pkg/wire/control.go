// Package wire defines Planck protocol v1: control messages carried as
// length-prefixed frames on the QUIC control stream, and the plaintext
// layouts of media datagrams carried over AEAD-sealed UDP.
package wire

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Version is the protocol version exchanged in Hello.
const Version = 1

// MaxControlLen bounds a control frame body, including its type byte.
const MaxControlLen = 1 << 16

const maxString = 1024

var (
	ErrTooLarge   = errors.New("wire: control message too large")
	ErrMalformed  = errors.New("wire: malformed message")
	ErrUnknownMsg = errors.New("wire: unknown message type")
)

type MsgType uint8

const (
	MsgHello MsgType = iota + 1
	MsgAuthRequest
	MsgAuthResponse
	MsgSessionConfig
	MsgSessionReady
	MsgPLI
	MsgRefInvalidate
	MsgStatsReport
	MsgGoodbye
)

// Message is a control-stream message. The set is closed: only types in this
// package implement it.
type Message interface {
	Type() MsgType
	appendBody(b []byte) []byte
	decodeBody(d *decoder)
}

type Role uint8

const (
	RoleHost Role = iota + 1
	RoleClient
)

type Hello struct {
	Version  uint16
	Role     Role
	Name     string
	Features uint32
}

type AuthMethod uint8

const (
	// AuthPaired: the client's certificate fingerprint was stored at pairing.
	AuthPaired AuthMethod = iota + 1
	// AuthPIN: first pairing; PINProof binds a shared PIN to this TLS session.
	AuthPIN
	// AuthTailscale: the host resolves the client's identity with Tailscale WhoIs.
	AuthTailscale
)

type AuthRequest struct {
	Method     AuthMethod
	DeviceName string
	PINProof   [32]byte
}

type AuthStatus uint8

const (
	AuthOK AuthStatus = iota + 1
	AuthDenied
	AuthPINRequired
)

type AuthResponse struct {
	Status   AuthStatus
	Identity string
	// PINProof is the host's proof of the PIN for AuthPIN, so the client
	// also detects a host that does not know it.
	PINProof [32]byte
	Message  string
}

type Codec uint8

const (
	CodecH264 Codec = iota + 1
	CodecHEVC
	CodecAV1
)

type Chroma uint8

const (
	Chroma420 Chroma = iota + 1
	Chroma444
)

type SessionConfig struct {
	Codec           Codec
	Chroma          Chroma
	Width           uint16
	Height          uint16
	FPS             uint16
	BitrateKbps     uint32
	Slices          uint8
	SubFrame        bool
	AudioSampleRate uint32
	AudioChannels   uint8
}

type SessionReady struct {
	SessionID uint64
	MediaPort uint16
	// ShardSize is the video FEC shard payload size for this session.
	ShardSize uint16
	Config    SessionConfig
}

// PLI asks the host for a keyframe.
type PLI struct{}

// RefInvalidate tells the host that FrameID and every later frame are
// unusable as references, so it can recover with a non-IDR frame.
type RefInvalidate struct {
	FrameID uint32
}

type StatsReport struct {
	RTTMicros       uint32
	JitterMicros    uint32
	LossPermille    uint16
	FramesReceived  uint32
	FramesDropped   uint32
	FramesRecovered uint32
	DecodeMicros    uint32
	ReceivedKbps    uint32
}

type GoodbyeReason uint8

const (
	GoodbyeNormal GoodbyeReason = iota + 1
	GoodbyeAuthFailed
	GoodbyeProtocolError
	GoodbyeTimeout
)

type Goodbye struct {
	Reason  GoodbyeReason
	Message string
}

func (*Hello) Type() MsgType         { return MsgHello }
func (*AuthRequest) Type() MsgType   { return MsgAuthRequest }
func (*AuthResponse) Type() MsgType  { return MsgAuthResponse }
func (*SessionConfig) Type() MsgType { return MsgSessionConfig }
func (*SessionReady) Type() MsgType  { return MsgSessionReady }
func (*PLI) Type() MsgType           { return MsgPLI }
func (*RefInvalidate) Type() MsgType { return MsgRefInvalidate }
func (*StatsReport) Type() MsgType   { return MsgStatsReport }
func (*Goodbye) Type() MsgType       { return MsgGoodbye }

func (m *Hello) appendBody(b []byte) []byte {
	b = binary.BigEndian.AppendUint16(b, m.Version)
	b = append(b, byte(m.Role))
	b = appendString(b, m.Name)
	return binary.BigEndian.AppendUint32(b, m.Features)
}

func (m *Hello) decodeBody(d *decoder) {
	m.Version = d.u16()
	m.Role = Role(d.u8())
	m.Name = d.str()
	m.Features = d.u32()
}

func (m *AuthRequest) appendBody(b []byte) []byte {
	b = append(b, byte(m.Method))
	b = appendString(b, m.DeviceName)
	return append(b, m.PINProof[:]...)
}

func (m *AuthRequest) decodeBody(d *decoder) {
	m.Method = AuthMethod(d.u8())
	m.DeviceName = d.str()
	d.bytes(m.PINProof[:])
}

func (m *AuthResponse) appendBody(b []byte) []byte {
	b = append(b, byte(m.Status))
	b = appendString(b, m.Identity)
	b = append(b, m.PINProof[:]...)
	return appendString(b, m.Message)
}

func (m *AuthResponse) decodeBody(d *decoder) {
	m.Status = AuthStatus(d.u8())
	m.Identity = d.str()
	d.bytes(m.PINProof[:])
	m.Message = d.str()
}

func (m *SessionConfig) appendBody(b []byte) []byte {
	b = append(b, byte(m.Codec), byte(m.Chroma))
	b = binary.BigEndian.AppendUint16(b, m.Width)
	b = binary.BigEndian.AppendUint16(b, m.Height)
	b = binary.BigEndian.AppendUint16(b, m.FPS)
	b = binary.BigEndian.AppendUint32(b, m.BitrateKbps)
	b = append(b, m.Slices, boolByte(m.SubFrame))
	b = binary.BigEndian.AppendUint32(b, m.AudioSampleRate)
	return append(b, m.AudioChannels)
}

func (m *SessionConfig) decodeBody(d *decoder) {
	m.Codec = Codec(d.u8())
	m.Chroma = Chroma(d.u8())
	m.Width = d.u16()
	m.Height = d.u16()
	m.FPS = d.u16()
	m.BitrateKbps = d.u32()
	m.Slices = d.u8()
	m.SubFrame = d.boolean()
	m.AudioSampleRate = d.u32()
	m.AudioChannels = d.u8()
}

func (m *SessionReady) appendBody(b []byte) []byte {
	b = binary.BigEndian.AppendUint64(b, m.SessionID)
	b = binary.BigEndian.AppendUint16(b, m.MediaPort)
	b = binary.BigEndian.AppendUint16(b, m.ShardSize)
	return m.Config.appendBody(b)
}

func (m *SessionReady) decodeBody(d *decoder) {
	m.SessionID = d.u64()
	m.MediaPort = d.u16()
	m.ShardSize = d.u16()
	m.Config.decodeBody(d)
}

func (*PLI) appendBody(b []byte) []byte { return b }
func (*PLI) decodeBody(*decoder)        {}

func (m *RefInvalidate) appendBody(b []byte) []byte {
	return binary.BigEndian.AppendUint32(b, m.FrameID)
}

func (m *RefInvalidate) decodeBody(d *decoder) { m.FrameID = d.u32() }

func (m *StatsReport) appendBody(b []byte) []byte {
	b = binary.BigEndian.AppendUint32(b, m.RTTMicros)
	b = binary.BigEndian.AppendUint32(b, m.JitterMicros)
	b = binary.BigEndian.AppendUint16(b, m.LossPermille)
	b = binary.BigEndian.AppendUint32(b, m.FramesReceived)
	b = binary.BigEndian.AppendUint32(b, m.FramesDropped)
	b = binary.BigEndian.AppendUint32(b, m.FramesRecovered)
	b = binary.BigEndian.AppendUint32(b, m.DecodeMicros)
	return binary.BigEndian.AppendUint32(b, m.ReceivedKbps)
}

func (m *StatsReport) decodeBody(d *decoder) {
	m.RTTMicros = d.u32()
	m.JitterMicros = d.u32()
	m.LossPermille = d.u16()
	m.FramesReceived = d.u32()
	m.FramesDropped = d.u32()
	m.FramesRecovered = d.u32()
	m.DecodeMicros = d.u32()
	m.ReceivedKbps = d.u32()
}

func (m *Goodbye) appendBody(b []byte) []byte {
	b = append(b, byte(m.Reason))
	return appendString(b, m.Message)
}

func (m *Goodbye) decodeBody(d *decoder) {
	m.Reason = GoodbyeReason(d.u8())
	m.Message = d.str()
}

// AppendMessage appends m as a control frame: a 32-bit big-endian length,
// then the type byte and the body.
func AppendMessage(b []byte, m Message) ([]byte, error) {
	start := len(b)
	b = append(b, 0, 0, 0, 0, byte(m.Type()))
	b = m.appendBody(b)
	n := len(b) - start - 4
	if n > MaxControlLen {
		return b[:start], ErrTooLarge
	}
	binary.BigEndian.PutUint32(b[start:], uint32(n))
	return b, nil
}

// WriteMessage writes m as one control frame.
func WriteMessage(w io.Writer, m Message) error {
	b, err := AppendMessage(make([]byte, 0, 64), m)
	if err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}

// ReadMessage reads one control frame.
func ReadMessage(r io.Reader) (Message, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n == 0 {
		return nil, ErrMalformed
	}
	if n > MaxControlLen {
		return nil, ErrTooLarge
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	return DecodeMessage(body)
}

// DecodeMessage decodes a frame body: the type byte and the message fields.
// Trailing bytes are rejected so a frame has exactly one encoding.
func DecodeMessage(body []byte) (Message, error) {
	if len(body) == 0 {
		return nil, ErrMalformed
	}
	m := newMessage(MsgType(body[0]))
	if m == nil {
		return nil, fmt.Errorf("%w: %d", ErrUnknownMsg, body[0])
	}
	d := decoder{b: body[1:]}
	m.decodeBody(&d)
	if d.bad || len(d.b) != 0 {
		return nil, ErrMalformed
	}
	return m, nil
}

func newMessage(t MsgType) Message {
	switch t {
	case MsgHello:
		return &Hello{}
	case MsgAuthRequest:
		return &AuthRequest{}
	case MsgAuthResponse:
		return &AuthResponse{}
	case MsgSessionConfig:
		return &SessionConfig{}
	case MsgSessionReady:
		return &SessionReady{}
	case MsgPLI:
		return &PLI{}
	case MsgRefInvalidate:
		return &RefInvalidate{}
	case MsgStatsReport:
		return &StatsReport{}
	case MsgGoodbye:
		return &Goodbye{}
	}
	return nil
}

func boolByte(v bool) byte {
	if v {
		return 1
	}
	return 0
}

func appendString(b []byte, s string) []byte {
	if len(s) > maxString {
		s = s[:maxString]
	}
	b = binary.BigEndian.AppendUint16(b, uint16(len(s)))
	return append(b, s...)
}

type decoder struct {
	b   []byte
	bad bool
}

func (d *decoder) take(n int) []byte {
	if d.bad || len(d.b) < n {
		d.bad = true
		return nil
	}
	v := d.b[:n]
	d.b = d.b[n:]
	return v
}

func (d *decoder) u8() uint8 {
	if v := d.take(1); v != nil {
		return v[0]
	}
	return 0
}

func (d *decoder) boolean() bool {
	switch d.u8() {
	case 0:
		return false
	case 1:
		return true
	}
	d.bad = true
	return false
}

func (d *decoder) u16() uint16 {
	if v := d.take(2); v != nil {
		return binary.BigEndian.Uint16(v)
	}
	return 0
}

func (d *decoder) u32() uint32 {
	if v := d.take(4); v != nil {
		return binary.BigEndian.Uint32(v)
	}
	return 0
}

func (d *decoder) u64() uint64 {
	if v := d.take(8); v != nil {
		return binary.BigEndian.Uint64(v)
	}
	return 0
}

func (d *decoder) bytes(dst []byte) {
	if v := d.take(len(dst)); v != nil {
		copy(dst, v)
	}
}

func (d *decoder) str() string {
	n := int(d.u16())
	if n > maxString {
		d.bad = true
		return ""
	}
	return string(d.take(n))
}
