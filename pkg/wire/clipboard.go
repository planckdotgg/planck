package wire

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// StreamClipboard is the first byte of a unidirectional QUIC stream that
// carries clipboard items. Each side opens one for what it sends.
const StreamClipboard byte = 0x01

// MaxClip bounds one clipboard item's bytes.
const MaxClip = 16 << 20

// ClipHeaderLen is the frame header in front of each item's bytes.
const ClipHeaderLen = 1 + 16 + 32 + 4

type ClipFormat uint8

const (
	// ClipText is UTF-8 text.
	ClipText ClipFormat = iota + 1
	// ClipPNG is a PNG image.
	ClipPNG
)

var (
	ErrClipTooLarge = errors.New("wire: clipboard item too large")
	ErrClipHash     = errors.New("wire: clipboard item does not match its hash")
	ErrClipFormat   = errors.New("wire: unknown clipboard format")
)

// Clip is one clipboard item. Origin identifies the device that first
// copied it, so an item is never applied back where it came from; Hash is
// the SHA-256 of Data.
type Clip struct {
	Format ClipFormat
	Origin [16]byte
	Hash   [32]byte
	Data   []byte
}

func (f ClipFormat) valid() bool { return f == ClipText || f == ClipPNG }

// NewClip makes an item from data and stamps its hash.
func NewClip(f ClipFormat, origin [16]byte, data []byte) *Clip {
	return &Clip{Format: f, Origin: origin, Hash: sha256.Sum256(data), Data: data}
}

// WriteClip writes c as one frame.
func WriteClip(w io.Writer, c *Clip) error {
	if !c.Format.valid() {
		return ErrClipFormat
	}
	if len(c.Data) > MaxClip {
		return ErrClipTooLarge
	}
	var hdr [ClipHeaderLen]byte
	hdr[0] = byte(c.Format)
	copy(hdr[1:17], c.Origin[:])
	copy(hdr[17:49], c.Hash[:])
	binary.BigEndian.PutUint32(hdr[49:], uint32(len(c.Data)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write(c.Data)
	return err
}

// ReadClip reads one frame. A frame longer than limit (at most MaxClip) is
// read and discarded and returns ErrClipTooLarge, and a frame whose bytes
// do not match its hash returns ErrClipHash; the stream stays usable after
// either. Any other error ends the stream.
func ReadClip(r io.Reader, limit int) (*Clip, error) {
	if limit <= 0 || limit > MaxClip {
		limit = MaxClip
	}
	var hdr [ClipHeaderLen]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	c := &Clip{Format: ClipFormat(hdr[0])}
	copy(c.Origin[:], hdr[1:17])
	copy(c.Hash[:], hdr[17:49])
	n := int64(binary.BigEndian.Uint32(hdr[49:]))
	if n > int64(limit) || !c.Format.valid() {
		if _, err := io.CopyN(io.Discard, r, n); err != nil {
			return nil, err
		}
		if !c.Format.valid() {
			return nil, fmt.Errorf("%w: %d", ErrClipFormat, hdr[0])
		}
		return nil, ErrClipTooLarge
	}
	c.Data = make([]byte, n)
	if _, err := io.ReadFull(r, c.Data); err != nil {
		return nil, err
	}
	if sha256.Sum256(c.Data) != c.Hash {
		return nil, ErrClipHash
	}
	return c, nil
}
