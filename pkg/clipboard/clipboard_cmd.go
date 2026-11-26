package clipboard

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// runner runs a command with stdin and returns at most maxOut+1 bytes of
// its stdout, so a read can tell an item is over MaxSize without holding it.
type runner func(ctx context.Context, stdin []byte, maxOut int, name string, args ...string) ([]byte, error)

// Which tools serve the clipboard: wl-clipboard under Wayland, xclip or
// xsel under X11.
const (
	toolWlPaste = "wl-paste"
	toolWlCopy  = "wl-copy"
	toolXclip   = "xclip"
	toolXsel    = "xsel"
)

const (
	cmdPollInterval = 300 * time.Millisecond
	cmdTimeout      = 3 * time.Second
	mimePNG         = "image/png"
	// passwordManagerHint is the target KeePassXC, Bitwarden and others set
	// on a secret they copied.
	passwordManagerHint = "x-kde-passwordManagerHint"
)

// Clipboard targets that carry text, best first: Wayland clients offer
// MIME types, X11 ones the older atoms.
var (
	textTargetsWayland = []string{"text/plain;charset=utf-8", "text/plain", "UTF8_STRING", "STRING", "TEXT"}
	textTargetsX11     = []string{"UTF8_STRING", "text/plain;charset=utf-8", "text/plain", "STRING", "TEXT"}
)

// cmdBoard is the clipboard of a Linux session, reached through the
// command-line tools of its display server.
type cmdBoard struct {
	wayland bool
	// xsel reads and writes text only.
	xsel     bool
	run      runner
	interval time.Duration

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

// sessionEnv is the part of the environment that picks the tools.
type sessionEnv struct {
	wayland, display string
	have             func(tool string) bool
}

// chooseTools picks the tools for a session: wl-clipboard when Wayland has
// it, else xclip or xsel against X11 (which XWayland also offers), and says
// what to install when none fits.
func chooseTools(e sessionEnv) (wayland, xsel bool, err error) {
	if e.wayland != "" && e.have(toolWlPaste) && e.have(toolWlCopy) {
		return true, false, nil
	}
	if e.display != "" {
		switch {
		case e.have(toolXclip):
			return false, false, nil
		case e.have(toolXsel):
			return false, true, nil
		}
	}
	switch {
	case e.wayland != "":
		return false, false, fmt.Errorf("%w: install wl-clipboard (wl-paste and wl-copy) for the Wayland clipboard", ErrUnsupported)
	case e.display != "":
		return false, false, fmt.Errorf("%w: install xclip or xsel for the X11 clipboard", ErrUnsupported)
	}
	return false, false, fmt.Errorf("%w: no Wayland or X11 session (WAYLAND_DISPLAY and DISPLAY are empty)", ErrUnsupported)
}

func (b *cmdBoard) call(stdin []byte, maxOut int, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
	defer cancel()
	return b.run(ctx, stdin, maxOut, name, args...)
}

// targets lists the formats on the clipboard; xsel cannot, so it is
// assumed to hold text.
func (b *cmdBoard) targets() ([]string, error) {
	var out []byte
	var err error
	switch {
	case b.wayland:
		out, err = b.call(nil, 1<<16, toolWlPaste, "--list-types")
	case b.xsel:
		return textTargetsX11[:1], nil
	default:
		out, err = b.call(nil, 1<<16, toolXclip, "-selection", "clipboard", "-o", "-t", "TARGETS")
	}
	if err != nil {
		return nil, err
	}
	return strings.Fields(string(out)), nil
}

func (b *cmdBoard) fetch(target string) ([]byte, error) {
	switch {
	case b.wayland:
		return b.call(nil, MaxSize, toolWlPaste, "--no-newline", "--type", target)
	case b.xsel:
		return b.call(nil, MaxSize, toolXsel, "--clipboard", "--output")
	}
	return b.call(nil, MaxSize, toolXclip, "-selection", "clipboard", "-o", "-t", target)
}

// pickTarget is the format to carry: text when offered, else a PNG; ok is
// false when the clipboard is empty, holds only formats Planck does not
// carry, or is marked secret.
func pickTarget(targets []string, wayland bool) (target string, f Format, ok bool) {
	if slices.Contains(targets, passwordManagerHint) {
		return "", 0, false
	}
	text := textTargetsX11
	if wayland {
		text = textTargetsWayland
	}
	for _, t := range text {
		if slices.Contains(targets, t) {
			return t, Text, true
		}
	}
	if slices.Contains(targets, mimePNG) {
		return mimePNG, PNG, true
	}
	return "", 0, false
}

func (b *cmdBoard) Read() (Item, bool, error) {
	targets, err := b.targets()
	if err != nil {
		// An empty clipboard makes the tools fail; that is not an error.
		return Item{}, false, nil
	}
	target, f, ok := pickTarget(targets, b.wayland)
	if !ok {
		return Item{}, false, nil
	}
	data, err := b.fetch(target)
	if err != nil {
		return Item{}, false, nil
	}
	if len(data) > MaxSize {
		return Item{Format: f, Size: len(data)}, true, nil
	}
	if f == Text && !utf8.Valid(data) {
		data = bytes.ToValidUTF8(data, []byte("�"))
	}
	return Item{Format: f, Data: data, Size: len(data)}, true, nil
}

func (b *cmdBoard) Write(it Item) error {
	if len(it.Data) > MaxSize {
		return ErrTooLarge
	}
	if it.Format != Text && it.Format != PNG {
		return ErrFormat
	}
	var name string
	var args []string
	switch {
	case b.wayland && it.Format == Text:
		name, args = toolWlCopy, []string{"--type", "text/plain;charset=utf-8"}
	case b.wayland:
		name, args = toolWlCopy, []string{"--type", mimePNG}
	case b.xsel && it.Format == Text:
		name, args = toolXsel, []string{"--clipboard", "--input"}
	case b.xsel:
		return fmt.Errorf("%w: xsel carries text only; install xclip for images", ErrUnsupported)
	case it.Format == Text:
		name, args = toolXclip, []string{"-selection", "clipboard", "-i", "-t", "UTF8_STRING"}
	default:
		name, args = toolXclip, []string{"-selection", "clipboard", "-i", "-t", mimePNG}
	}
	data := it.Data
	if data == nil {
		data = []byte{}
	}
	if _, err := b.call(data, 0, name, args...); err != nil {
		return fmt.Errorf("clipboard: %s: %w", name, err)
	}
	return nil
}

// signature identifies the clipboard's content for change detection.
func signature(it Item, ok bool) [32]byte {
	if !ok {
		return [32]byte{}
	}
	h := sha256.New()
	h.Write([]byte{byte(it.Format)})
	h.Write(it.Data)
	var s [32]byte
	h.Sum(s[:0])
	return s
}

// Watch polls the clipboard: the command-line tools offer no change
// notification a portable program can rely on (wl-paste --watch needs a
// data-control protocol that GNOME lacks, and xclip has none). It returns at
// once and reports changes from a goroutine until ctx ends or Close.
func (b *cmdBoard) Watch(ctx context.Context, onChange func(Item)) error {
	b.mu.Lock()
	if b.done != nil {
		b.mu.Unlock()
		return errWatching
	}
	ctx, b.cancel = context.WithCancel(ctx)
	done := make(chan struct{})
	b.done = done
	b.mu.Unlock()
	it, ok, _ := b.Read()
	last := signature(it, ok)
	go func() {
		defer func() {
			close(done)
			b.mu.Lock()
			b.done = nil
			b.mu.Unlock()
		}()
		t := time.NewTicker(b.interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			it, ok, _ := b.Read()
			if sig := signature(it, ok); sig != last {
				last = sig
				if ok {
					onChange(it)
				}
			}
		}
	}()
	return nil
}

func (b *cmdBoard) Close() error {
	b.mu.Lock()
	cancel, done := b.cancel, b.done
	b.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
	return nil
}

var _ Board = (*cmdBoard)(nil)
