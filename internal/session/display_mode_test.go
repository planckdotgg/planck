package session

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/planckdotgg/planck/pkg/audio"
	"github.com/planckdotgg/planck/pkg/capture/cursor"
	"github.com/planckdotgg/planck/pkg/encode"
	"github.com/planckdotgg/planck/pkg/input"
)

// modePipe is a fakePipe on a display whose picture size the test changes,
// as a duplication recovered in place after a mode switch has it: the
// display rectangle it opened with stays.
type modePipe struct {
	fakePipe
	info *atomic.Pointer[VideoInfo]
}

func (p *modePipe) Info() VideoInfo { return *p.info.Load() }

type areaInjector struct {
	mu               sync.Mutex
	display, desktop input.Rect
}

func (i *areaInjector) Inject(input.Event) error { return nil }
func (i *areaInjector) Close() error             { return nil }
func (i *areaInjector) SetTarget(d, dk input.Rect) {
	i.mu.Lock()
	i.display, i.desktop = d, dk
	i.mu.Unlock()
}

func (i *areaInjector) target() (input.Rect, input.Rect) {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.display, i.desktop
}

type modeLayout struct{ display, desktop input.Rect }

func cursorRect(r input.Rect) cursor.Rect { return cursor.Rect{X: r.X, Y: r.Y, W: r.W, H: r.H} }

// TestInputAreaFollowsModeSwitch: a full-screen game switching the
// captured display from 5120x1440 to 3840x2160 under a running stream, a
// lone display or one right of the primary, moves absolute input and the
// cursor's place onto the new mode, whether the pipe recovered in place or
// the system's layout lagged the desktop the cursor saw.
func TestInputAreaFollowsModeSwitch(t *testing.T) {
	const id = `\\.\DISPLAY32`
	for _, c := range []struct {
		name          string
		before, after modeLayout
		lag           int
	}{
		{"single display", modeLayout{rect(0, 0, 5120, 1440), rect(0, 0, 5120, 1440)},
			modeLayout{rect(0, 0, 3840, 2160), rect(0, 0, 3840, 2160)}, 0},
		{"right of the primary", modeLayout{rect(1920, 0, 5120, 1440), rect(0, 0, 7040, 1440)},
			modeLayout{rect(1920, 0, 3840, 2160), rect(0, 0, 5760, 2160)}, 0},
		{"layout lagging the desktop", modeLayout{rect(0, 0, 5120, 1440), rect(0, 0, 5120, 1440)},
			modeLayout{rect(0, 0, 3840, 2160), rect(0, 0, 3840, 2160)}, 8},
		// Other displays hold the desktop's bounds through the switch, so
		// only the captured display's size tells the layout caught up.
		{"desktop unchanged, layout lagging", modeLayout{rect(1920, 0, 5120, 1440), rect(0, 0, 9600, 2160)},
			modeLayout{rect(1920, 0, 3840, 2160), rect(0, 0, 9600, 2160)}, 8},
	} {
		t.Run(c.name, func(t *testing.T) {
			var info atomic.Pointer[VideoInfo]
			info.Store(&VideoInfo{Width: 5120, Height: 1440, FPS: 60, Source: "display " + id, DisplayID: id,
				Display: c.before.display, Desktop: c.before.desktop})
			var layout atomic.Pointer[modeLayout]
			layout.Store(&c.before)
			var desk atomic.Pointer[cursor.Rect]
			d0 := cursorRect(c.before.desktop)
			desk.Store(&d0)
			var stale atomic.Int32
			inj := &areaInjector{}
			h, err := NewHost(HostConfig{
				ConfigDir: t.TempDir(), Name: "test-host", ListenAddr: "127.0.0.1:0", DiscoveryAddr: "off", NoTailscale: true,
				FPS: 60, BitrateKbps: 10_000, PIN: "246810", InjectInput: true, Injector: inj,
				OpenVideo: func(p VideoParams) (VideoPipe, error) {
					return &modePipe{fakePipe{fps: p.FPS, forced: new(atomic.Int64), kbps: new(atomic.Int64)}, &info}, nil
				},
				OpenAudio: func() (audio.Capturer, error) { return &fakeCapturer{}, nil },
				ReadCursor: func() (cursor.State, error) {
					return cursor.State{X: 1920, Y: 1080, Desktop: *desk.Load()}, nil
				},
				InputArea: func(got string) (input.Rect, input.Rect, error) {
					if got != id {
						t.Errorf("asked for display %q", got)
					}
					if stale.Add(-1) >= 0 {
						return c.before.display, c.before.desktop, nil
					}
					l := layout.Load()
					return l.display, l.desktop, nil
				},
				Logf: t.Logf,
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := h.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(h.Stop)
			c2, err := NewClient(ClientConfig{
				ConfigDir: t.TempDir(), Name: "test-client", PIN: "246810", NoAudio: true,
				Quality: Quality{Codec: encode.HEVC, FPS: 60}, OpenSink: NullSinkOpener(&NullSink{}), Tailscale: noTailscale{}, Logf: t.Logf,
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(c2.Disconnect)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := c2.Connect(ctx, Target{Addr: h.Addr().String()}); err != nil {
				t.Fatal(err)
			}
			waitFor(t, "input on the first mode", func() bool {
				d, dk := inj.target()
				return d == c.before.display && dk == c.before.desktop
			})

			stale.Store(int32(c.lag))
			layout.Store(&c.after)
			d1 := cursorRect(c.after.desktop)
			desk.Store(&d1)
			recovered := *info.Load()
			recovered.Width, recovered.Height = 3840, 2160
			info.Store(&recovered)

			waitFor(t, "input on the new mode", func() bool {
				d, dk := inj.target()
				return d == c.after.display && dk == c.after.desktop
			})
			st := h.current()
			st.mu.Lock()
			got := st.info
			st.mu.Unlock()
			if got.Display != c.after.display || got.Desktop != c.after.desktop || got.Width != 3840 || got.Height != 2160 {
				t.Fatalf("stream info %+v, want %+v on 3840x2160", got, c.after)
			}
			m := cursorMessage(cursor.State{X: c.after.display.X + 1920, Y: 1080}, cursorRect(got.Display))
			if !m.HasPos || m.X < 32760 || m.X > 32790 || m.Y < 32760 || m.Y > 32790 {
				t.Fatalf("the display's centre normalizes to %+v", m)
			}
		})
	}
}

// TestPipeOpenKeepsNewerArea: the cursor loop finding the display's new
// mode while the video loop opens a pipe that saw the old one, the
// injector ends on the area the stream ends with, never the pipe's older
// one under the stream's newer.
func TestPipeOpenKeepsNewerArea(t *testing.T) {
	const id = `\\.\DISPLAY32`
	old := VideoInfo{Width: 5120, Height: 1440, DisplayID: id, Display: rect(1920, 0, 5120, 1440), Desktop: rect(0, 0, 7040, 1440)}
	now := modeLayout{rect(1920, 0, 3840, 2160), rect(0, 0, 5760, 2160)}
	inj := &areaInjector{}
	h := &Host{logf: t.Logf, inject: inj}
	h.cfg.InputArea = func(string) (input.Rect, input.Rect, error) { return now.display, now.desktop, nil }
	st := &stream{h: h}
	var info atomic.Pointer[VideoInfo]
	info.Store(&old)
	pipe := &modePipe{fakePipe{fps: 60, forced: new(atomic.Int64), kbps: new(atomic.Int64)}, &info}

	// The pipe's open stops in openPhase, after it made its area the
	// stream's, until the cursor loop has taken the newer one.
	st.phaseMu.Lock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		st.opened(pipe)
	}()
	waitFor(t, "the pipe's area published", st.areaStale.Load)
	st.syncInputArea()
	st.phaseMu.Unlock()
	<-done

	d, dk := inj.target()
	st.mu.Lock()
	got := st.info
	st.mu.Unlock()
	if got.Display != now.display || got.Desktop != now.desktop {
		t.Fatalf("stream area %+v of %+v, want the newer %+v", got.Display, got.Desktop, now)
	}
	if d != got.Display || dk != got.Desktop {
		t.Fatalf("injector on %+v of %+v, stream on %+v of %+v", d, dk, got.Display, got.Desktop)
	}
}
