package session

import (
	"errors"
	"runtime"
	"sync"
	"time"

	"github.com/planckdotgg/planck/pkg/audio"
	"github.com/planckdotgg/planck/pkg/input"
	"github.com/planckdotgg/planck/pkg/power"
	"github.com/planckdotgg/planck/pkg/transport"
	"github.com/planckdotgg/planck/pkg/vdd"
)

// SessionSettings are what hosting does around a client's session. Each
// is off in the zero value: tests must never lock, mute or blank the
// machine they run on. Host.SetSessionSettings changes them live.
type SessionSettings struct {
	// StayAwake keeps the machine and its display awake while hosting.
	StayAwake bool
	// LockOnDisconnect locks the machine when a client leaves or is
	// disconnected for idling; not when the host's own user ends it.
	LockOnDisconnect bool
	// MuteSpeakers mutes the default output while a client streams, and
	// puts its mute back after.
	MuteSpeakers bool
	// PrivacyMode turns the machine's own displays off while a client
	// streams from a virtual display, from the next one opened.
	PrivacyMode bool
	// IdleTimeout disconnects a client that sent no input for that long;
	// 0 is never.
	IdleTimeout time.Duration
}

// SystemHooks are the OS actions SessionSettings take. A nil field uses
// the platform's.
type SystemHooks struct {
	KeepAwake    func() (release func(), err error)
	Lock         func() error
	MuteSpeakers func() (restore func() error, err error)
	// Isolate leaves the display capture opens, by name, the only one on.
	Isolate func(display string) (restore func() error, err error)
	// AppPIDs finds the processes of HostConfig.AudioExcludeApps.
	AppPIDs func(names []string) ([]int, error)
	// SendSAS asks the system for Ctrl+Alt+Del, which injected keys
	// cannot give.
	SendSAS func() error
}

func (s SystemHooks) withDefaults() SystemHooks {
	if s.KeepAwake == nil {
		s.KeepAwake = func() (func(), error) { return power.KeepAwake("Planck is hosting") }
	}
	if s.Lock == nil {
		s.Lock = power.LockScreen
	}
	if s.MuteSpeakers == nil {
		s.MuteSpeakers = audio.MuteOutput
	}
	if s.Isolate == nil {
		s.Isolate = vdd.IsolateDisplay
	}
	if s.AppPIDs == nil {
		s.AppPIDs = audio.AppPIDs
	}
	if s.SendSAS == nil {
		s.SendSAS = input.SendSAS
	}
	return s
}

// ErrPrivacyMode is why privacy mode cannot work on this machine.
var ErrPrivacyMode = errors.New("privacy mode needs a virtual display, which needs a Windows host with SudoVDA")

// PrivacyModeProblem is why privacy mode cannot work on this machine, ""
// when it can.
func PrivacyModeProblem() string {
	if runtime.GOOS != "windows" || !vdd.Available() {
		return ErrPrivacyMode.Error()
	}
	return ""
}

// ErrNoClient is a kick with no client streaming.
var ErrNoClient = errors.New("session: no client is streaming")

// hostSys is the live SessionSettings and what they hold.
type hostSys struct {
	mu       sync.Mutex
	set      SessionSettings
	hooks    SystemHooks
	started  bool
	awake    func()
	idleTick time.Duration
}

// SessionSettings are the live ones.
func (h *Host) SessionSettings() SessionSettings {
	h.sys.mu.Lock()
	defer h.sys.mu.Unlock()
	return h.sys.set
}

// SetSessionSettings changes the session settings at once: the stay
// awake hold, the speakers of the stream running, and the idle timeout.
// Privacy mode takes effect from the next virtual display.
func (h *Host) SetSessionSettings(s SessionSettings) {
	h.sys.mu.Lock()
	h.sys.set = s
	h.sys.mu.Unlock()
	h.applyAwake()
	if st := h.current(); st != nil {
		st.applyMute(s.MuteSpeakers)
	}
}

// applyAwake holds the machine awake while started with StayAwake, and
// lets go otherwise.
func (h *Host) applyAwake() {
	h.sys.mu.Lock()
	defer h.sys.mu.Unlock()
	want := h.sys.started && h.sys.set.StayAwake
	switch {
	case want && h.sys.awake == nil:
		release, err := h.sys.hooks.KeepAwake()
		if err != nil {
			h.logf("planck: stay awake: %v", err)
			return
		}
		h.sys.awake = release
	case !want && h.sys.awake != nil:
		h.sys.awake()
		h.sys.awake = nil
	}
}

func (h *Host) setStarted(on bool) {
	h.sys.mu.Lock()
	h.sys.started = on
	h.sys.mu.Unlock()
	h.applyAwake()
}

// KickClient disconnects the client streaming now; it may connect again.
func (h *Host) KickClient() error {
	h.mu.Lock()
	st := h.cur
	h.cur = nil
	h.mu.Unlock()
	if st == nil {
		return ErrNoClient
	}
	h.logf("planck: %s disconnected by this machine's user", st.sess.Identity())
	h.stopStream(st)
	return nil
}

// streamEnded locks the machine after a client left on its own or idled
// out, when the settings say to.
func (h *Host) streamEnded(st *stream) {
	if st.endedByHost.Load() {
		return
	}
	h.sys.mu.Lock()
	lock := h.sys.set.LockOnDisconnect
	h.sys.mu.Unlock()
	if !lock {
		return
	}
	if err := h.sys.hooks.Lock(); err != nil {
		h.logf("planck: lock this machine: %v", err)
	}
}

// applyMute mutes the speakers for this stream, or puts them back.
func (st *stream) applyMute(on bool) {
	st.muteMu.Lock()
	defer st.muteMu.Unlock()
	switch {
	case on && st.unmute == nil && !st.ended:
		restore, err := st.h.sys.hooks.MuteSpeakers()
		if err != nil {
			st.h.logf("planck: mute this machine's speakers: %v", err)
			return
		}
		st.unmute = restore
	case !on && st.unmute != nil:
		if err := st.unmute(); err != nil {
			st.h.logf("planck: unmute this machine's speakers: %v", err)
		}
		st.unmute = nil
	}
}

// endMute puts the speakers back for good as the stream ends.
func (st *stream) endMute() {
	st.applyMute(false)
	st.muteMu.Lock()
	st.ended = true
	st.muteMu.Unlock()
}

// isolate turns the other displays off while the stream captures vd.
func (st *stream) isolate(vd VirtualDisplay) {
	st.h.sys.mu.Lock()
	on := st.h.sys.set.PrivacyMode
	st.h.sys.mu.Unlock()
	if !on || st.restoreDisplays != nil {
		return
	}
	restore, err := st.h.sys.hooks.Isolate(vd.Name())
	if err != nil {
		st.h.logf("planck: privacy mode: %v", err)
		return
	}
	st.restoreDisplays = restore
	st.h.logf("planck: privacy mode: only %s is on", vd.Name())
}

// unisolate turns the other displays back on.
func (st *stream) unisolate() {
	if st.restoreDisplays == nil {
		return
	}
	if err := st.restoreDisplays(); err != nil {
		st.h.logf("planck: privacy mode: %v", err)
	}
	st.restoreDisplays = nil
}

// idleExpired reports whether the client sent no input for longer than
// the idle timeout, at now.
func (h *Host) idleExpired(now int64) bool {
	h.sys.mu.Lock()
	d := h.sys.set.IdleTimeout
	h.sys.mu.Unlock()
	return d > 0 && time.Duration(now-h.lastInput.Load()) > d
}

// idleLoop disconnects the client once it idles out.
func (st *stream) idleLoop() {
	tick := st.h.sys.idleTick
	if tick <= 0 {
		tick = time.Second
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-st.ctx.Done():
			return
		case <-t.C:
			if st.h.idleExpired(st.h.now()) {
				st.h.logf("planck: %s sent no input for the idle timeout; disconnecting", st.sess.Identity())
				st.sess.Close()
				return
			}
		}
	}
}

// now is the host's clock, transport.Mono unless a test sets another.
func (h *Host) now() int64 {
	if h.clock != nil {
		return h.clock()
	}
	return transport.Mono()
}

// secureAttention asks the system for Ctrl+Alt+Del when a client sends
// it, on Windows, where injected keys cannot give it.
func (h *Host) secureAttention(ev input.Event) {
	if runtime.GOOS != "windows" || !input.IsSecureAttention(ev) {
		return
	}
	if err := h.sys.hooks.SendSAS(); err != nil {
		h.logf("planck: Ctrl+Alt+Del: %v", err)
	}
}

// audioExcludePIDs are the processes the stream's sound leaves out: the
// configured PIDs and the running processes of the configured apps.
func (h *Host) audioExcludePIDs() []int {
	pids := append([]int(nil), h.cfg.AudioExcludePIDs...)
	if len(h.cfg.AudioExcludeApps) == 0 {
		return pids
	}
	if !audio.CanExcludeProcesses {
		h.logf("planck: leaving %v out of the sound needs a macOS host; this one streams everything but Planck", h.cfg.AudioExcludeApps)
		return pids
	}
	found, err := h.sys.hooks.AppPIDs(h.cfg.AudioExcludeApps)
	if err != nil {
		h.logf("planck: find %v to leave out of the sound: %v", h.cfg.AudioExcludeApps, err)
	}
	return append(pids, found...)
}
