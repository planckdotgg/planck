package main

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/planckdotgg/planck/internal/session"
	"github.com/planckdotgg/planck/internal/ui"
)

// How long Settings shows a probe of the machine before asking again.
// The pages draw every frame, and a probe can take 100 ms (ScreenCaptureKit
// asks a system service), so a stale answer is served while a fresh one
// loads in the background.
const (
	displaysTTL     = 5 * time.Second
	audioDevicesTTL = 3 * time.Second
	virtualAudioTTL = 5 * time.Second
	runningAppsTTL  = 3 * time.Second
	privacyTTL      = 5 * time.Second
)

// ttlCache keeps load's last answer for ttl. The first get loads in the
// caller; after that a stale get returns the old answer at once and
// reloads in one goroutine, calling onUpdate when the answer changed.
type ttlCache[T any] struct {
	ttl      time.Duration
	load     func() T
	equal    func(a, b T) bool
	onUpdate func()
	now      func() time.Time

	mu         sync.Mutex
	val        T
	at         time.Time
	valid      bool
	refreshing bool
	// gen counts invalidations, so a reload started before one is dropped.
	gen uint64
}

func newTTLCache[T any](ttl time.Duration, load func() T, equal func(a, b T) bool, onUpdate func()) *ttlCache[T] {
	return &ttlCache[T]{ttl: ttl, load: load, equal: equal, onUpdate: onUpdate, now: time.Now}
}

func (c *ttlCache[T]) get() T {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.valid {
		c.val, c.at, c.valid = c.load(), c.now(), true
		return c.val
	}
	if c.now().Sub(c.at) >= c.ttl && !c.refreshing {
		c.refreshing = true
		go c.refresh(c.gen)
	}
	return c.val
}

func (c *ttlCache[T]) refresh(gen uint64) {
	v := c.load()
	c.mu.Lock()
	if c.gen != gen {
		c.mu.Unlock()
		return
	}
	same := c.equal(c.val, v)
	c.val, c.at, c.refreshing = v, c.now(), false
	c.mu.Unlock()
	if !same && c.onUpdate != nil {
		c.onUpdate()
	}
}

// invalidate makes the next get load again in the caller.
func (c *ttlCache[T]) invalidate() {
	c.mu.Lock()
	c.valid, c.refreshing = false, false
	c.gen++
	c.mu.Unlock()
}

// probeCaches hold the machine probes Settings draws from each frame.
type probeCaches struct {
	displays     *ttlCache[[]ui.DisplayInfo]
	audioDevices *ttlCache[ui.AudioDevices]
	virtualAudio *ttlCache[ui.VirtualAudioStatus]
	runningApps  *ttlCache[[]string]
	privacy      *ttlCache[string]
	paired       pairedFileCache
}

func (b *backend) initCaches() {
	b.caches = probeCaches{
		displays: newTTLCache(displaysTTL, b.loadDisplays, slices.Equal[[]ui.DisplayInfo], b.changed),
		audioDevices: newTTLCache(audioDevicesTTL, b.loadAudioDevices, func(x, y ui.AudioDevices) bool {
			return slices.Equal(x.Outputs, y.Outputs) && slices.Equal(x.Inputs, y.Inputs)
		}, b.changed),
		virtualAudio: newTTLCache(virtualAudioTTL, b.loadVirtualAudio, func(x, y ui.VirtualAudioStatus) bool { return x == y }, b.changed),
		runningApps:  newTTLCache(runningAppsTTL, b.loadRunningApps, slices.Equal[[]string], b.changed),
		privacy:      newTTLCache(privacyTTL, session.PrivacyModeProblem, func(x, y string) bool { return x == y }, b.changed),
	}
}

func (b *backend) Displays() []ui.DisplayInfo { return slices.Clone(b.caches.displays.get()) }

func (b *backend) AudioDevices() ui.AudioDevices {
	d := b.caches.audioDevices.get()
	return ui.AudioDevices{Outputs: slices.Clone(d.Outputs), Inputs: slices.Clone(d.Inputs)}
}

func (b *backend) VirtualAudio() ui.VirtualAudioStatus { return b.caches.virtualAudio.get() }

func (b *backend) RunningApps() []string { return slices.Clone(b.caches.runningApps.get()) }

func (b *backend) PrivacyModeProblem() string { return b.caches.privacy.get() }

// pairedFileCache keeps paired-clients.json's clients while the file's
// size and modification time stay the same.
type pairedFileCache struct {
	mu    sync.Mutex
	key   pairedFileKey
	all   map[string]session.PairedClient
	valid bool
}

type pairedFileKey struct {
	exists bool
	size   int64
	mod    time.Time
}

func (p *pairedFileCache) load(dir string, logf func(string, ...any)) map[string]session.PairedClient {
	var key pairedFileKey
	if fi, err := os.Stat(filepath.Join(dir, session.PairedClientsFile)); err == nil {
		key = pairedFileKey{exists: true, size: fi.Size(), mod: fi.ModTime()}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.valid && p.key == key {
		return maps.Clone(p.all)
	}
	all, err := session.LoadPairedClients(dir)
	if err != nil {
		logf("planck: paired clients: %v", err)
		p.valid = false
		return all
	}
	p.key, p.all, p.valid = key, all, true
	return maps.Clone(all)
}

func (p *pairedFileCache) invalidate() {
	p.mu.Lock()
	p.valid, p.all = false, nil
	p.mu.Unlock()
}
