package session

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/planckdotgg/planck/pkg/clipboard"
	"github.com/planckdotgg/planck/pkg/transport"
	"github.com/planckdotgg/planck/pkg/wire"
)

// ClipboardConfig is one side's clipboard sharing, per direction.
type ClipboardConfig struct {
	// Send shares this device's clipboard changes with the peer.
	Send bool
	// Receive applies the peer's clipboard changes here.
	Receive bool
}

func (c ClipboardConfig) any() bool { return c.Send || c.Receive }

// ClipboardStats count items; contents are never logged or kept.
type ClipboardStats struct {
	Sent, Received, Skipped uint64
}

// clipSync shares one device's clipboard with whichever session is live.
// It sends only while this side sends and the peer said it receives, and
// applies only while this side receives; a Guard stops loops.
type clipSync struct {
	board clipboard.Board
	guard *clipboard.Guard
	logf  func(string, ...any)
	// session is the live session, or nil.
	session func() *transport.Session
	// peerReceives says the live session's peer applies items.
	peerReceives func() bool

	cfg     atomic.Pointer[ClipboardConfig]
	pending chan clipboard.Item
	cancel  context.CancelFunc
	done    chan struct{}

	sent, received, skipped atomic.Uint64
}

func startClipSync(board clipboard.Board, cfg ClipboardConfig, session func() *transport.Session, peerReceives func() bool, logf func(string, ...any)) (*clipSync, error) {
	s := &clipSync{board: board, guard: clipboard.NewGuard(), logf: logf, session: session, peerReceives: peerReceives,
		pending: make(chan clipboard.Item, 1), done: make(chan struct{})}
	s.cfg.Store(&cfg)
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	if err := board.Watch(ctx, s.changed); err != nil {
		cancel()
		return nil, err
	}
	go s.sendLoop(ctx)
	return s, nil
}

func (s *clipSync) config() ClipboardConfig { return *s.cfg.Load() }

func (s *clipSync) setConfig(c ClipboardConfig) { s.cfg.Store(&c) }

// changed runs on the clipboard's watcher; the send happens elsewhere so a
// large image never holds up the watcher. Only the newest change waits.
func (s *clipSync) changed(it clipboard.Item) {
	if !s.config().Send {
		return
	}
	select {
	case s.pending <- it:
	default:
		select {
		case <-s.pending:
		default:
		}
		s.pending <- it
	}
}

func (s *clipSync) sendLoop(ctx context.Context) {
	defer close(s.done)
	for {
		var it clipboard.Item
		select {
		case <-ctx.Done():
			return
		case it = <-s.pending:
		}
		sess := s.session()
		if sess == nil || !s.config().Send || !s.peerReceives() {
			continue
		}
		if it.TooLarge() {
			s.skipped.Add(1)
			s.logf("planck: clipboard item of %d MiB not shared (the limit is 16 MiB)", it.Size>>20)
			continue
		}
		hash, ok := s.guard.Outgoing(it.Data)
		if !ok {
			continue
		}
		c := &wire.Clip{Format: wire.ClipFormat(it.Format), Origin: s.guard.Origin(), Hash: hash, Data: it.Data}
		sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err := sess.SendClipboard(sctx, c)
		cancel()
		switch {
		case err == nil:
			s.sent.Add(1)
		case errors.Is(err, transport.ErrPeerUnsupported):
		default:
			s.logf("planck: clipboard not shared: %v", err)
		}
	}
}

// incoming applies a peer's item.
func (s *clipSync) incoming(c *wire.Clip) {
	if !s.config().Receive || !s.guard.Incoming(c.Origin, c.Hash) {
		return
	}
	if err := s.board.Write(clipboard.Item{Format: clipboard.Format(c.Format), Data: c.Data, Size: len(c.Data)}); err != nil {
		s.logf("planck: clipboard not applied: %v", err)
		return
	}
	s.received.Add(1)
}

func (s *clipSync) stats() ClipboardStats {
	return ClipboardStats{Sent: s.sent.Load(), Received: s.received.Load(), Skipped: s.skipped.Load()}
}

func (s *clipSync) close() {
	s.cancel()
	<-s.done
	s.board.Close()
}

// openClipSync opens the board with open (clipboard.Open when nil) and
// starts sharing; a device without a clipboard backend shares nothing.
func openClipSync(open func() (clipboard.Board, error), cfg ClipboardConfig, session func() *transport.Session, peerReceives func() bool, logf func(string, ...any)) *clipSync {
	if open == nil {
		open = clipboard.Open
	}
	board, err := open()
	if err != nil {
		logf("planck: clipboard sharing unavailable: %v", err)
		return nil
	}
	s, err := startClipSync(board, cfg, session, peerReceives, logf)
	if err != nil {
		board.Close()
		logf("planck: clipboard sharing unavailable: %v", err)
		return nil
	}
	return s
}
