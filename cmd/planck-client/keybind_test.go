package main

import (
	"context"
	"errors"
	"runtime"
	"slices"
	"sync"
	"testing"

	mui "github.com/egoist/mygo/ui"
	"github.com/planckdotgg/planck/internal/session"
	"github.com/planckdotgg/planck/internal/ui"
	"github.com/planckdotgg/planck/pkg/audio"
	"github.com/planckdotgg/planck/pkg/config"
	"github.com/planckdotgg/planck/pkg/input"
)

func rebound(t *testing.T, set map[ui.HotkeyAction]string, never ...string) *localKeys {
	t.Helper()
	c := config.DefaultClientConfig()
	for a, ch := range set {
		if err := c.SetHotkey(a, ch, "darwin"); err != nil {
			t.Fatal(err)
		}
	}
	for _, ch := range never {
		if err := c.AddNeverSend(ch); err != nil {
			t.Fatal(err)
		}
	}
	l := &localKeys{}
	l.update(hotkeysOf(c), c.NeverSend)
	return l
}

func sentKeys(evs []input.Event) []uint16 {
	var out []uint16
	for _, ev := range evs {
		if ev.Type == input.TypeKey && ev.Key.Pressed && modifierBit(ev.Key.Usage) == 0 {
			out = append(out, ev.Key.Usage)
		}
	}
	return out
}

// TestForwarderKeepsLocalChords: a rebound hotkey and a never-sent chord
// stay here; the chord a hotkey left goes to the host again.
func TestForwarderKeepsLocalChords(t *testing.T) {
	var sent []input.Event
	f := &inputForwarder{send: func(ev input.Event) { sent = append(sent, ev) },
		local: rebound(t, map[ui.HotkeyAction]string{ui.HotkeyMetrics: "Ctrl+Alt+J"}, "Alt+Shift+Q")}
	press := func(k mui.Key, m mui.Modifiers) []uint16 {
		sent = nil
		f.keyboard([]mui.Key{k}, m)
		f.keyboard(nil, 0)
		return sentKeys(sent)
	}
	if got := press(mui.KeyJ, mui.Ctrl|mui.Alt); len(got) != 0 {
		t.Errorf("the rebound metrics chord reached the host: %#x", got)
	}
	if got := press(mui.KeyM, mui.Ctrl|mui.Alt); !slices.Equal(got, []uint16{input.KeyM}) {
		t.Errorf("Ctrl+Alt+M, no longer a hotkey, sent %#x", got)
	}
	if got := press(mui.KeyQ, mui.Alt|mui.Shift); len(got) != 0 {
		t.Errorf("a never-sent chord reached the host: %#x", got)
	}
	if got := press(mui.KeyQ, mui.Alt); !slices.Equal(got, []uint16{input.KeyQ}) {
		t.Errorf("Alt+Q sent %#x", got)
	}
	if got := press(mui.KeyO, mui.Ctrl|mui.Alt); len(got) != 0 {
		t.Errorf("the stream menu's chord reached the host: %#x", got)
	}
}

// TestSystemKeysNeverSend: in immersive mode a never-sent chord goes to
// this device's OS, not the host, Cmd included though the OS's chord
// lacks it; the same key without the chord goes to the host.
func TestSystemKeysNeverSend(t *testing.T) {
	r := newSysRig()
	r.sys.local = rebound(t, nil, "Super+Shift+5")
	r.sys.setEnabled(true)
	r.sys.frame(true)
	if r.tap.press(input.KeyLeftGUI, true, 0) {
		t.Fatal("Cmd reached the OS")
	}
	if !r.tap.press(input.Key5, true, mui.Shift) || !r.tap.press(input.Key5, false, mui.Shift) {
		t.Fatal("Cmd+Shift+5 was kept from the OS")
	}
	r.tap.press(input.KeyLeftGUI, false, 0)
	if got := sentKeys(r.take()); len(got) != 0 {
		t.Fatalf("the never-sent chord reached the host: %#x", got)
	}
	if r.tap.press(input.Key5, true, mui.Shift) {
		t.Fatal("Shift+5 reached the OS")
	}
	r.tap.press(input.Key5, false, mui.Shift)
	if got := sentKeys(r.take()); !slices.Equal(got, []uint16{input.Key5}) {
		t.Fatalf("Shift+5 sent %#x", got)
	}
}

type fakeShortcuts struct {
	mu  sync.Mutex
	fns map[string]func()
}

func (f *fakeShortcuts) Register(acc string, fn func()) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fns == nil {
		f.fns = map[string]func(){}
	}
	f.fns[acc] = fn
	return nil
}

func (f *fakeShortcuts) Unregister(acc string) { f.mu.Lock(); delete(f.fns, acc); f.mu.Unlock() }

func (f *fakeShortcuts) keys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for k := range f.fns {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// TestBackendHotkeys: hotkeys persist, a conflict names its owner, the
// filters hear each change, and the host's hotkeys are the desktop's while
// hosting, rebound at once, and gone when hosting stops.
func TestBackendHotkeys(t *testing.T) {
	dir := t.TempDir()
	b, _ := newTestBackend(t, dir, nil)
	globals := &fakeShortcuts{}
	b.globals = globals
	var heard [][]string
	b.onKeys = func(_ []ui.Hotkey, never []string) { heard = append(heard, never) }

	if err := b.SetHotkey(ui.HotkeyMetrics, "Ctrl+Alt+J"); err != nil {
		t.Fatal(err)
	}
	var he *ui.HotkeyError
	if err := b.SetHotkey(ui.HotkeyKeyframe, "Ctrl+Alt+J"); !errors.As(err, &he) || he.Owner != ui.HotkeyMetrics {
		t.Fatalf("conflict: %v", err)
	}
	if err := b.AddNeverSend("F13"); err != nil {
		t.Fatal(err)
	}
	if len(heard) != 2 || !slices.Equal(heard[1], []string{"F13"}) {
		t.Errorf("the filters heard %v", heard)
	}
	again, _ := newTestBackend(t, dir, nil)
	if got := chordOf(again.Hotkeys(), ui.HotkeyMetrics); got != "Ctrl+Alt+J" {
		t.Errorf("metrics after a restart: %q", got)
	}
	if got := again.NeverSend(); !slices.Equal(got, []string{"F13"}) {
		t.Errorf("never send after a restart: %v", got)
	}

	if len(globals.keys()) != 0 {
		t.Fatalf("global hotkeys before hosting: %v", globals.keys())
	}
	if err := b.SetHosting(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if got := globals.keys(); !slices.Equal(got, []string{"Ctrl+F3"}) {
		t.Errorf("global hotkeys while hosting: %v", got)
	}
	if err := b.SetHotkey(ui.HotkeyKickClient, "Ctrl+F4"); err != nil {
		t.Fatal(err)
	}
	if got := globals.keys(); !slices.Equal(got, []string{"Ctrl+F3", "Ctrl+F4"}) {
		t.Errorf("after binding kick: %v", got)
	}
	globals.mu.Lock()
	stop := globals.fns["Ctrl+F3"]
	globals.mu.Unlock()
	stop()
	waitUntil(t, "hosting to stop", func() bool { return !b.Hosting().On })
	waitUntil(t, "the global hotkeys to go", func() bool { return len(globals.keys()) == 0 })
	if err := b.KickClient(); err == nil {
		t.Error("kick while not hosting succeeded")
	}
}

func chordOf(hs []ui.Hotkey, a ui.HotkeyAction) string {
	for _, h := range hs {
		if h.Action == a {
			return h.Chord
		}
	}
	return ""
}

// TestBackendSwapCmdCtrl: with the swap on, Cmd goes to a Windows host as
// Ctrl; off, or to a host of this machine's OS family, it goes as is.
func TestBackendSwapCmdCtrl(t *testing.T) {
	b, _ := newTestBackend(t, t.TempDir(), nil)
	c, err := session.NewClient(session.ClientConfig{ConfigDir: t.TempDir(), Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	cmd := input.Event{Type: input.TypeKey, Key: input.KeyEvent{Usage: input.KeyLeftGUI, Pressed: true, Modifiers: input.ModLeftGUI}}
	other := "darwin"
	if runtime.GOOS == "darwin" {
		other = "windows"
	}
	b.hostOSOf, b.hostOS = c, other
	if got := b.swapInput(c, cmd); got != cmd {
		t.Errorf("swap off changed %+v to %+v", cmd.Key, got.Key)
	}
	s := b.Settings()
	s.SwapCmdCtrl = true
	if err := b.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	if got, want := b.swapInput(c, cmd), input.SwapCmdCtrl(cmd); got != want {
		t.Errorf("swap on to a %s host: %+v, want %+v", other, got.Key, want.Key)
	}
	b.hostOS = runtime.GOOS
	if got := b.swapInput(c, cmd); got != cmd {
		t.Errorf("swap on to a %s host changed %+v", runtime.GOOS, cmd.Key)
	}
	if stored, err := config.LoadClientConfig(b.dir); err != nil || !stored.SwapCmdCtrl {
		t.Errorf("stored swap %v, %v", stored.SwapCmdCtrl, err)
	}
}

// TestBackendAudioDevicesAndApps: the pickers' lists come from the
// machine's, and each app is named once.
func TestBackendAudioDevicesAndApps(t *testing.T) {
	dir := t.TempDir()
	b, err := newBackend(backendConfig{dir: dir, finder: newFinder("", nil, nil, t.Logf), logf: t.Logf,
		audioOutputs: func() ([]audio.Device, error) {
			return []audio.Device{{ID: "o1", Name: "Speakers", IsDefault: true}}, nil
		},
		audioInputs: func() ([]audio.Device, error) { return nil, errors.New("no inputs") },
		runningApps: func() ([]audio.App, error) {
			return []audio.App{{PID: 1, Name: "discord"}, {PID: 2, Name: "Discord"}, {PID: 3, Name: "Music"}, {PID: 4, Name: "Arc"}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	devs := b.AudioDevices()
	if len(devs.Outputs) != 1 || devs.Outputs[0] != (ui.AudioDevice{ID: "o1", Name: "Speakers", IsDefault: true}) || devs.Inputs != nil {
		t.Errorf("devices %+v", devs)
	}
	if got := b.RunningApps(); !slices.Equal(got, []string{"Arc", "discord", "Music"}) {
		t.Errorf("apps %v", got)
	}
	s := b.Settings()
	s.OutputDevice, s.MicDevice = "o1", "mic-9"
	if err := b.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	stored, err := config.LoadClientConfig(dir)
	if err != nil || stored.AudioOutputDevice != "o1" || stored.MicDevice != "mic-9" {
		t.Errorf("stored devices %q %q, %v", stored.AudioOutputDevice, stored.MicDevice, err)
	}
}
