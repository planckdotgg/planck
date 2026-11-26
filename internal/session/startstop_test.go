package session

import (
	"context"
	"errors"
	"net"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/planckdotgg/planck/pkg/clipboard"
	"github.com/planckdotgg/planck/pkg/gamepad"
	"github.com/planckdotgg/planck/pkg/input"
	"github.com/planckdotgg/planck/pkg/transport"
)

// wedgedBoard is a clipboard whose Close hangs until release closes.
type wedgedBoard struct {
	memBoard
	entered chan struct{}
	release chan struct{}
}

func (b *wedgedBoard) Close() error {
	close(b.entered)
	<-b.release
	return nil
}

// TestStopWithWedgedClipboardCloseClosesVDD: a clipboard that hangs on
// close does not keep Stop from closing the virtual display driver.
func TestStopWithWedgedClipboardCloseClosesVDD(t *testing.T) {
	board := &wedgedBoard{entered: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(func() { close(board.release) })
	logs := &logBuf{}
	h := newTestHost(t, HostConfig{Clipboard: ClipboardConfig{Send: true, Receive: true},
		OpenClipboard: func() (clipboard.Board, error) { return board, nil }}, logs)
	h.stopWait = 200 * time.Millisecond
	var vddClosed atomic.Bool
	h.closeVDD = func() error {
		vddClosed.Store(true)
		return nil
	}
	startTestHost(t, h)

	go h.Stop()
	select {
	case <-h.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("a wedged clipboard close kept the host from stopping")
	}
	select {
	case <-board.entered:
	default:
		t.Fatal("Stop never closed the clipboard")
	}
	if !vddClosed.Load() {
		t.Fatal("a wedged clipboard close kept Stop from closing the virtual display driver")
	}
	if !logs.has("to close; stopping without them") {
		t.Error("the host did not log the slow clipboard close")
	}
}

// TestStartOpensInjectorBeforeListening: no client can reach the host's
// input handler until the injector it uses is in place, and input works
// once Start returns.
func TestStartOpensInjectorBeforeListening(t *testing.T) {
	h := newTestHost(t, HostConfig{InjectInput: true}, &logBuf{})
	var listened atomic.Bool
	h.listen = func(addr string, cfg transport.ServerConfig) (*transport.Server, error) {
		listened.Store(true)
		return transport.Listen(addr, cfg)
	}
	inj := &closeInjector{}
	entered, release := make(chan struct{}), make(chan struct{})
	h.openInjector = func() (input.Injector, error) {
		close(entered)
		<-release
		return inj, nil
	}
	started := make(chan error, 1)
	go func() { started <- h.Start(context.Background()) }()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("Start never opened the injector")
	}
	if listened.Load() {
		close(release)
		<-started
		h.Stop()
		t.Fatal("the host listened for clients before its injector was open")
	}
	close(release)
	if err := <-started; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Stop)

	r := newClient(t, t.TempDir(), h.PIN())
	if err := connect(t, r, h.Addr().String()); err != nil {
		t.Fatal(err)
	}
	press := input.Event{Type: input.TypeKey, Key: input.KeyEvent{Usage: input.KeyA, Pressed: true}}
	if err := r.client.SendInput(press); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the press at the host", func() bool { return slices.Equal(inj.events(), []input.Event{press}) })
}

// TestStartClosesInjectorWhenListenFails: a Start that cannot listen
// closes the injector it opened.
func TestStartClosesInjectorWhenListenFails(t *testing.T) {
	h := newTestHost(t, HostConfig{InjectInput: true}, &logBuf{})
	inj := &closeInjector{}
	h.openInjector = func() (input.Injector, error) { return inj, nil }
	h.listen = func(string, transport.ServerConfig) (*transport.Server, error) {
		return nil, errors.New("port taken")
	}
	if err := h.Start(context.Background()); err == nil {
		h.Stop()
		t.Fatal("Start succeeded without a listener")
	}
	if !inj.closed.Load() {
		t.Fatal("a failed Start left its injector open")
	}
}

// TestStartRetryAfterBindFailureDeliversInput: a Start that failed to bind
// its control port leaves nothing behind that drops input once a retried
// Start on the same host binds it.
func TestStartRetryAfterBindFailureDeliversInput(t *testing.T) {
	h := newTestHost(t, HostConfig{InjectInput: true}, &logBuf{})
	var mu sync.Mutex
	var inj *closeInjector
	h.openInjector = func() (input.Injector, error) {
		mu.Lock()
		defer mu.Unlock()
		inj = &closeInjector{}
		return inj, nil
	}
	taken, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	h.cfg.ListenAddr = taken.LocalAddr().String()
	if err := h.Start(context.Background()); err == nil {
		taken.Close()
		h.Stop()
		t.Fatal("Start bound a control port that was taken")
	}
	taken.Close()
	startTestHost(t, h)

	r := newClient(t, t.TempDir(), h.PIN())
	if err := connect(t, r, h.Addr().String()); err != nil {
		t.Fatal(err)
	}
	press := input.Event{Type: input.TypeKey, Key: input.KeyEvent{Usage: input.KeyA, Pressed: true}}
	if err := r.client.SendInput(press); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	last := inj
	mu.Unlock()
	waitFor(t, "the press at the host after a retried Start", func() bool {
		return slices.Equal(last.events(), []input.Event{press})
	})
}

// TestSetGamepadSettingsAfterStop: changing the virtual controller type
// after Stop closed the injector does not reach the injector.
func TestSetGamepadSettingsAfterStop(t *testing.T) {
	h := newTestHost(t, HostConfig{InjectInput: true}, &logBuf{})
	pads := newPadInjector()
	h.openInjector = func() (input.Injector, error) { return pads, nil }
	startTestHost(t, h)
	before, _ := pads.state()
	h.Stop()

	next := gamepad.PadDS4
	if before == next {
		next = gamepad.PadXbox360
	}
	if err := h.SetGamepadSettings(GamepadHostSettings{Allow: true, Type: next}); err != nil {
		t.Fatal(err)
	}
	if typ, _ := pads.state(); typ != before {
		t.Fatalf("SetGamepadSettings after Stop set the closed injector's type to %v", typ)
	}
	if got := h.GamepadSettings(); got.Type != next {
		t.Fatalf("settings after Stop: %+v; want type %v", got, next)
	}
}
