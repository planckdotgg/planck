// Package transport carries Planck sessions. Listen and Dial run the
// production session layer: a pinned-certificate QUIC control stream
// (ALPN planck-v1) and an AEAD-sealed raw UDP media plane with adaptive FEC,
// rate control and clock sync. Under it are the primitives: AEAD packet
// framing keyed from a TLS 1.3 exporter, pacing, network impairment, and the
// Phase 0 spike packetizer and reassembler used by cmd/spike-transport.
package transport

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"errors"
	"fmt"

	"golang.org/x/crypto/chacha20poly1305"
)

type Suite uint8

const (
	SuiteAES128GCM Suite = iota + 1
	SuiteChaCha20Poly1305
)

func (s Suite) String() string {
	switch s {
	case SuiteAES128GCM:
		return "aes128gcm"
	case SuiteChaCha20Poly1305:
		return "chacha20poly1305"
	}
	return fmt.Sprintf("suite(%d)", uint8(s))
}

func ParseSuite(name string) (Suite, error) {
	switch name {
	case "aes128gcm", "aes":
		return SuiteAES128GCM, nil
	case "chacha20poly1305", "chacha":
		return SuiteChaCha20Poly1305, nil
	}
	return 0, fmt.Errorf("unknown AEAD suite %q", name)
}

func (s Suite) keyLen() int {
	if s == SuiteChaCha20Poly1305 {
		return chacha20poly1305.KeySize
	}
	return 16
}

const (
	ivLen = 12
	// AEADHeaderLen is the cleartext prefix of every sealed packet:
	// one marker byte and a 64-bit big-endian sequence number.
	AEADHeaderLen = 1 + 8
	AEADTagLen    = 16
	// AEADOverhead is the number of bytes sealing adds to a plaintext.
	AEADOverhead = AEADHeaderLen + AEADTagLen

	packetMarker = 0x50
)

var (
	ErrShortPacket = errors.New("transport: packet too short")
	ErrBadMarker   = errors.New("transport: bad packet marker")
	ErrReplay      = errors.New("transport: replayed or stale packet")
	ErrAuth        = errors.New("transport: packet authentication failed")
)

// DirectionKey is the key material for one direction of a media session.
type DirectionKey struct {
	Suite Suite
	Key   []byte
	IV    [ivLen]byte
}

func newAEAD(k DirectionKey) (cipher.AEAD, error) {
	switch k.Suite {
	case SuiteAES128GCM:
		block, err := aes.NewCipher(k.Key)
		if err != nil {
			return nil, err
		}
		return cipher.NewGCM(block)
	case SuiteChaCha20Poly1305:
		return chacha20poly1305.New(k.Key)
	}
	return nil, fmt.Errorf("transport: unsupported suite %v", k.Suite)
}

// Sealer encrypts outgoing packets. It is not safe for concurrent use.
type Sealer struct {
	aead  cipher.AEAD
	iv    [ivLen]byte
	nonce [ivLen]byte
	seq   uint64
}

func NewSealer(k DirectionKey) (*Sealer, error) {
	a, err := newAEAD(k)
	if err != nil {
		return nil, err
	}
	return &Sealer{aead: a, iv: k.IV}, nil
}

// Seal writes header || ciphertext || tag for plaintext into dst and
// returns the sealed packet. dst must have capacity for
// len(plaintext)+AEADOverhead; plaintext may alias dst[AEADHeaderLen:].
func (s *Sealer) Seal(dst, plaintext []byte) []byte {
	s.seq++
	dst = dst[:AEADHeaderLen]
	dst[0] = packetMarker
	binary.BigEndian.PutUint64(dst[1:], s.seq)
	makeNonce(&s.nonce, &s.iv, s.seq)
	return s.aead.Seal(dst, s.nonce[:], plaintext, dst[:AEADHeaderLen])
}

// Opener authenticates and decrypts incoming packets, rejecting replays
// with a 64-packet sliding window. It is not safe for concurrent use.
type Opener struct {
	aead   cipher.AEAD
	iv     [ivLen]byte
	nonce  [ivLen]byte
	replay ReplayWindow
}

func NewOpener(k DirectionKey) (*Opener, error) {
	a, err := newAEAD(k)
	if err != nil {
		return nil, err
	}
	return &Opener{aead: a, iv: k.IV}, nil
}

// Open decrypts pkt in place and returns the plaintext, which aliases pkt.
func (o *Opener) Open(pkt []byte) ([]byte, error) {
	if len(pkt) < AEADOverhead {
		return nil, ErrShortPacket
	}
	if pkt[0] != packetMarker {
		return nil, ErrBadMarker
	}
	seq := binary.BigEndian.Uint64(pkt[1:AEADHeaderLen])
	if !o.replay.Check(seq) {
		return nil, ErrReplay
	}
	makeNonce(&o.nonce, &o.iv, seq)
	body := pkt[AEADHeaderLen:]
	pt, err := o.aead.Open(body[:0], o.nonce[:], body, pkt[:AEADHeaderLen])
	if err != nil {
		return nil, ErrAuth
	}
	o.replay.Accept(seq)
	return pt, nil
}

func makeNonce(nonce, iv *[ivLen]byte, seq uint64) {
	*nonce = *iv
	for i := 0; i < 8; i++ {
		nonce[ivLen-1-i] ^= byte(seq >> (8 * i))
	}
}

// ReplayWindow tracks the highest accepted sequence number and a bitmap of
// the 64 numbers below it. Sequence 0 is never valid.
type ReplayWindow struct {
	top    uint64
	bitmap uint64
}

func (w *ReplayWindow) Check(seq uint64) bool {
	if seq == 0 {
		return false
	}
	if seq > w.top {
		return true
	}
	diff := w.top - seq
	if diff >= 64 {
		return false
	}
	return w.bitmap&(1<<diff) == 0
}

// Accept records seq; call it only after the packet authenticated.
func (w *ReplayWindow) Accept(seq uint64) {
	if seq > w.top {
		shift := seq - w.top
		if shift >= 64 {
			w.bitmap = 0
		} else {
			w.bitmap <<= shift
		}
		w.bitmap |= 1
		w.top = seq
		return
	}
	w.bitmap |= 1 << (w.top - seq)
}
