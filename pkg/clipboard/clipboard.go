// Package clipboard reads, writes and watches the system clipboard for
// sharing it between host and client. It carries UTF-8 text and PNG
// images, skips content that password managers mark as concealed or
// transient, and never logs what it carries.
package clipboard

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"sync"
)

// MaxSize bounds one item's bytes.
const MaxSize = 16 << 20

type Format uint8

const (
	// Text is UTF-8 text.
	Text Format = iota + 1
	// PNG is a PNG image.
	PNG
)

// Item is one clipboard content in its canonical form. Size is the
// content's size; an item larger than MaxSize has Size set and no Data.
type Item struct {
	Format Format
	Data   []byte
	Size   int
}

// TooLarge says the item was left out for its size.
func (it Item) TooLarge() bool { return it.Size > MaxSize }

var (
	ErrUnsupported = errors.New("clipboard: not supported on this platform")
	ErrTooLarge    = errors.New("clipboard: item larger than 16 MiB")
	ErrFormat      = errors.New("clipboard: unknown format")
)

// Board is the system clipboard.
type Board interface {
	// Watch calls onChange for each change another app makes, in canonical
	// form (text before an image when both are offered), until ctx ends.
	// Content marked concealed or transient is skipped. One watcher at a
	// time.
	Watch(ctx context.Context, onChange func(Item)) error
	// Read returns the current content; ok is false when there is none
	// Planck carries, or it is concealed.
	Read() (it Item, ok bool, err error)
	Write(it Item) error
	Close() error
}

// Open opens the system clipboard.
func Open() (Board, error) { return openBoard() }

// Guard stops clipboard loops between two devices. Each device stamps the
// items it sends with its own origin; a device drops items from its own
// origin, does not send back what it just applied, and does not apply what
// it just sent. It remembers only the last transfer, either way, so content
// copied again after the other side changed the clipboard still goes out.
type Guard struct {
	origin [16]byte

	mu      sync.Mutex
	applied [32]byte
	sent    [32]byte
}

func NewGuard() *Guard {
	g := &Guard{}
	_, _ = rand.Read(g.origin[:])
	return g
}

func (g *Guard) Origin() [16]byte { return g.origin }

// Outgoing hashes a local change and says whether to send it: not when it
// is the remote item this device just applied (its own write coming back)
// or the item it sent last.
func (g *Guard) Outgoing(data []byte) (hash [32]byte, send bool) {
	hash = sha256.Sum256(data)
	g.mu.Lock()
	defer g.mu.Unlock()
	if hash == g.applied || hash == g.sent {
		return hash, false
	}
	g.sent, g.applied = hash, [32]byte{}
	return hash, true
}

// Incoming says whether to apply a remote item, and records it as applied
// when so.
func (g *Guard) Incoming(origin [16]byte, hash [32]byte) bool {
	if origin == g.origin {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if hash == g.applied || hash == g.sent {
		return false
	}
	g.applied, g.sent = hash, [32]byte{}
	return true
}

var errWatching = errors.New("clipboard: already watched")
