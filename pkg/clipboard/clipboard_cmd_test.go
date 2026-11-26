package clipboard

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeTools is a clipboard behind a runner: targets maps a target to its
// content, and every call is logged.
type fakeTools struct {
	mu      sync.Mutex
	content map[string][]byte
	calls   []string
	written map[string][]byte
	fail    bool
}

func (f *fakeTools) set(content map[string][]byte) {
	f.mu.Lock()
	f.content = content
	f.mu.Unlock()
}

func (f *fakeTools) run(_ context.Context, stdin []byte, maxOut int, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cmd := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, cmd)
	if f.fail {
		return nil, errors.New("Nothing is copied")
	}
	if stdin != nil {
		if f.written == nil {
			f.written = map[string][]byte{}
		}
		f.written[cmd] = stdin
		return nil, nil
	}
	if strings.Contains(cmd, "--list-types") || strings.HasSuffix(cmd, "-t TARGETS") {
		var ts []string
		for t := range f.content {
			ts = append(ts, t)
		}
		slices.Sort(ts)
		return []byte(strings.Join(ts, "\n") + "\n"), nil
	}
	target := args[len(args)-1]
	if name == toolXsel {
		target = textTargetsX11[0]
	}
	data, ok := f.content[target]
	if !ok {
		return nil, errors.New("no such target")
	}
	if len(data) > maxOut {
		return data[:maxOut+1], nil
	}
	return data, nil
}

func newFake(wayland, xsel bool) (*cmdBoard, *fakeTools) {
	f := &fakeTools{}
	return &cmdBoard{wayland: wayland, xsel: xsel, run: f.run, interval: 5 * time.Millisecond}, f
}

func TestChooseTools(t *testing.T) {
	have := func(tools ...string) func(string) bool {
		return func(t string) bool { return slices.Contains(tools, t) }
	}
	cases := []struct {
		name         string
		env          sessionEnv
		wayland, xsl bool
		err          bool
	}{
		{"wayland", sessionEnv{wayland: "wayland-0", display: ":0", have: have("wl-paste", "wl-copy", "xclip")}, true, false, false},
		{"wayland without wl-clipboard falls back to XWayland", sessionEnv{wayland: "wayland-0", display: ":0", have: have("xclip")}, false, false, false},
		{"wayland and nothing", sessionEnv{wayland: "wayland-0", have: have()}, false, false, true},
		{"x11 xclip", sessionEnv{display: ":0", have: have("xclip", "xsel")}, false, false, false},
		{"x11 xsel", sessionEnv{display: ":0", have: have("xsel")}, false, true, false},
		{"x11 nothing", sessionEnv{display: ":0", have: have()}, false, false, true},
		{"console", sessionEnv{have: have("xclip")}, false, false, true},
	}
	for _, c := range cases {
		w, x, err := chooseTools(c.env)
		if (err != nil) != c.err || w != c.wayland || x != c.xsl {
			t.Errorf("%s: wayland %v xsel %v err %v", c.name, w, x, err)
		}
		if err != nil && !errors.Is(err, ErrUnsupported) {
			t.Errorf("%s: %v is not ErrUnsupported", c.name, err)
		}
	}
}

func TestPickTarget(t *testing.T) {
	cases := []struct {
		targets []string
		wayland bool
		want    string
		f       Format
		ok      bool
	}{
		{[]string{"text/plain", "text/plain;charset=utf-8", "image/png"}, true, "text/plain;charset=utf-8", Text, true},
		{[]string{"TARGETS", "STRING", "UTF8_STRING", "text/plain;charset=utf-8"}, false, "UTF8_STRING", Text, true},
		{[]string{"image/png", "image/bmp"}, true, "image/png", PNG, true},
		{[]string{"image/jpeg"}, true, "", 0, false},
		{[]string{"text/plain", "x-kde-passwordManagerHint"}, true, "", 0, false},
		{nil, false, "", 0, false},
	}
	for _, c := range cases {
		got, f, ok := pickTarget(c.targets, c.wayland)
		if got != c.want || f != c.f || ok != c.ok {
			t.Errorf("pickTarget(%v, %v) = %q %v %v", c.targets, c.wayland, got, f, ok)
		}
	}
}

func TestReadText(t *testing.T) {
	for _, wayland := range []bool{true, false} {
		b, f := newFake(wayland, false)
		f.set(map[string][]byte{"text/plain;charset=utf-8": []byte("héllo"), "UTF8_STRING": []byte("héllo"), "image/png": []byte("png")})
		it, ok, err := b.Read()
		if err != nil || !ok || it.Format != Text || string(it.Data) != "héllo" || it.Size != 6 {
			t.Errorf("wayland=%v: %+v %v %v", wayland, it, ok, err)
		}
		want := "xclip -selection clipboard -o -t UTF8_STRING"
		if wayland {
			want = "wl-paste --no-newline --type text/plain;charset=utf-8"
		}
		if last := f.calls[len(f.calls)-1]; last != want {
			t.Errorf("wayland=%v: ran %q, want %q", wayland, last, want)
		}
	}
}

func TestReadPNGAndEmpty(t *testing.T) {
	b, f := newFake(true, false)
	f.set(map[string][]byte{"image/png": []byte("\x89PNG")})
	if it, ok, _ := b.Read(); !ok || it.Format != PNG || string(it.Data) != "\x89PNG" {
		t.Errorf("png: %+v %v", it, ok)
	}
	f.fail = true
	if _, ok, err := b.Read(); ok || err != nil {
		t.Errorf("empty clipboard: ok %v err %v", ok, err)
	}
}

func TestReadSkipsSecretsAndRefusesOversize(t *testing.T) {
	b, f := newFake(true, false)
	f.set(map[string][]byte{"text/plain": []byte("hunter2"), passwordManagerHint: []byte("secret")})
	if _, ok, _ := b.Read(); ok {
		t.Error("a password manager's copy was read")
	}
	f.set(map[string][]byte{"text/plain": make([]byte, MaxSize+1)})
	it, ok, _ := b.Read()
	if !ok || !it.TooLarge() || it.Data != nil {
		t.Errorf("oversize: size %d data %d ok %v", it.Size, len(it.Data), ok)
	}
}

func TestReadFixesInvalidUTF8(t *testing.T) {
	b, f := newFake(false, false)
	f.set(map[string][]byte{"UTF8_STRING": {'a', 0xff, 'b'}})
	it, _, _ := b.Read()
	if string(it.Data) != "a�b" {
		t.Errorf("data %q", it.Data)
	}
}

func TestXselReadsText(t *testing.T) {
	b, f := newFake(false, true)
	f.set(map[string][]byte{"UTF8_STRING": []byte("x")})
	if it, ok, _ := b.Read(); !ok || string(it.Data) != "x" {
		t.Errorf("xsel read %+v %v", it, ok)
	}
	if err := b.Write(Item{Format: PNG, Data: []byte("p")}); !errors.Is(err, ErrUnsupported) {
		t.Errorf("xsel image write: %v", err)
	}
}

func TestWrite(t *testing.T) {
	cases := []struct {
		wayland, xsel bool
		it            Item
		cmd           string
	}{
		{true, false, Item{Format: Text, Data: []byte("a")}, "wl-copy --type text/plain;charset=utf-8"},
		{true, false, Item{Format: PNG, Data: []byte("p")}, "wl-copy --type image/png"},
		{false, false, Item{Format: Text, Data: []byte("a")}, "xclip -selection clipboard -i -t UTF8_STRING"},
		{false, false, Item{Format: PNG, Data: []byte("p")}, "xclip -selection clipboard -i -t image/png"},
		{false, true, Item{Format: Text, Data: []byte("a")}, "xsel --clipboard --input"},
		{false, false, Item{Format: Text}, "xclip -selection clipboard -i -t UTF8_STRING"},
	}
	for _, c := range cases {
		b, f := newFake(c.wayland, c.xsel)
		if err := b.Write(c.it); err != nil {
			t.Errorf("%s: %v", c.cmd, err)
			continue
		}
		got, ok := f.written[c.cmd]
		if !ok || string(got) != string(c.it.Data) {
			t.Errorf("%s: wrote %q (ran %v)", c.cmd, got, f.calls)
		}
	}
	b, _ := newFake(true, false)
	if err := b.Write(Item{Format: 9}); !errors.Is(err, ErrFormat) {
		t.Errorf("bad format: %v", err)
	}
	if err := b.Write(Item{Format: Text, Data: make([]byte, MaxSize+1)}); !errors.Is(err, ErrTooLarge) {
		t.Errorf("too large: %v", err)
	}
}

func TestWatchReportsOnlyChanges(t *testing.T) {
	b, f := newFake(false, false)
	f.set(map[string][]byte{"UTF8_STRING": []byte("before")})
	got := make(chan Item, 8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := b.Watch(ctx, func(it Item) { got <- it }); err != nil {
		t.Fatal(err)
	}
	if err := b.Watch(ctx, func(Item) {}); !errors.Is(err, errWatching) {
		t.Errorf("second watcher: %v", err)
	}
	select {
	case it := <-got:
		t.Fatalf("reported the content that was there already: %+v", it)
	case <-time.After(40 * time.Millisecond):
	}
	f.set(map[string][]byte{"UTF8_STRING": []byte("after")})
	select {
	case it := <-got:
		if string(it.Data) != "after" {
			t.Errorf("reported %q", it.Data)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no change reported")
	}
	f.set(map[string][]byte{})
	f.set(map[string][]byte{"UTF8_STRING": []byte("after")})
	select {
	case it := <-got:
		t.Errorf("same content reported again: %+v", it)
	case <-time.After(40 * time.Millisecond):
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if err := b.Watch(context.Background(), func(Item) {}); err != nil {
		t.Errorf("watch after close: %v", err)
	}
	b.Close()
}

func TestSignature(t *testing.T) {
	a := signature(Item{Format: Text, Data: []byte("x")}, true)
	if a == signature(Item{Format: PNG, Data: []byte("x")}, true) || a == signature(Item{}, false) {
		t.Error("signatures collide")
	}
}
