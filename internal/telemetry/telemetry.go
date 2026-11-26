// Package telemetry sends anonymous usage signals to TelemetryDeck (Ingest
// API v2): that Planck was installed and launched, and that a stream started
// and ended, with its codec and the kind of route it took. A signal never
// holds a host or device name, an address, a Tailscale identity, a window
// title, a pairing PIN or key, or anything of the stream itself; the payload
// keeps only values from the fixed lists below.
//
// The user turns it off in Settings › General. Off stops every signal at
// once, including queued ones and a send in flight.
package telemetry

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// DefaultAppID is the TelemetryDeck App ID of the official app. Empty sends
// nothing. Release builds set it with -ldflags "-X
// github.com/planckdotgg/planck/internal/telemetry.DefaultAppID=<id>";
// PLANCK_TELEMETRY_APP_ID overrides it.
var DefaultAppID = ""

// DefaultEndpoint is TelemetryDeck's Ingest API v2.
const DefaultEndpoint = "https://nom.telemetrydeck.com/v2/"

// The signal types.
const (
	TypeInstalled      = "app.installed"
	TypeLaunched       = "app.launched"
	TypeSessionStarted = "session.started"
	TypeSessionEnded   = "session.ended"
)

// The build channels: an official release, or any other build.
const (
	ChannelRelease = "release"
	ChannelDev     = "dev"
)

// The modes: which side of a stream this process, or a session, is.
const (
	ModeClient = "client"
	ModeHost   = "host"
)

// The values of a session's transportKind.
const (
	TransportLAN             = "lan"
	TransportTailscaleDirect = "tailscale_direct"
	TransportTailscaleRelay  = "tailscale_relay"
	TransportUnknown         = unknown
)

const unknown = "unknown"

const platformTag = "desktop"

// maxQueued bounds the signals kept while TelemetryDeck cannot be reached.
const maxQueued = 50

// Options set up a Client.
type Options struct {
	AppID    string
	Endpoint string
	TestMode bool
	// StateFile keeps the install ID and whether app.installed was sent.
	StateFile  string
	AppVersion string
	Channel    string
	// Mode is ModeClient or ModeHost: the program that sends.
	Mode string
	HTTP *http.Client
	// RetryAfter is the wait after a failed send; 0 is one minute.
	RetryAfter time.Duration
}

// OptionsForThisBuild are the options of this build: the App ID, the
// endpoint, and test mode from the build, overridden by
// PLANCK_TELEMETRY_APP_ID, PLANCK_TELEMETRY_ENDPOINT and
// PLANCK_TELEMETRY_TEST_MODE=1. A build that is not official is always in
// test mode: the variable can only turn test mode on.
func OptionsForThisBuild(configDir, version string, official bool, mode string) Options {
	o := Options{AppID: DefaultAppID, Endpoint: DefaultEndpoint, TestMode: !official,
		StateFile: filepath.Join(configDir, "telemetry.json"), AppVersion: version, Channel: ChannelDev, Mode: mode}
	if official {
		o.Channel = ChannelRelease
	}
	if v := strings.TrimSpace(os.Getenv("PLANCK_TELEMETRY_APP_ID")); v != "" {
		o.AppID = v
	}
	if v := strings.TrimSpace(os.Getenv("PLANCK_TELEMETRY_ENDPOINT")); v != "" {
		o.Endpoint = v
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("PLANCK_TELEMETRY_TEST_MODE"))) {
	case "1", "true":
		o.TestMode = true
	}
	return o
}

// Session is what session.started and session.ended tell of a stream.
// Unknown values are sent as "unknown", so nothing else can leave.
type Session struct {
	Mode      string // ModeClient or ModeHost
	Codec     string // CodecName's input: "HEVC", "h264", "PyroWave"...
	Transport string // TransportKind's input: "lan-direct", "tailscale-derp"...
}

// CodecName is codec as one of h264, hevc, av1 or pyrowave, else unknown.
func CodecName(codec string) string {
	switch strings.ToLower(strings.NewReplacer(".", "", "-", "", " ", "").Replace(codec)) {
	case "h264", "avc":
		return "h264"
	case "hevc", "h265":
		return "hevc"
	case "av1":
		return "av1"
	case "pyrowave":
		return "pyrowave"
	}
	return unknown
}

// TransportKind is a path's name, as pkg/ratectl or pkg/discovery give it,
// as one of the Transport constants.
func TransportKind(path string) string {
	switch strings.ToLower(strings.ReplaceAll(strings.TrimSpace(path), "_", "-")) {
	case "lan", "lan-direct":
		return TransportLAN
	case "tailscale-direct":
		return TransportTailscaleDirect
	case "tailscale-relay", "tailscale-derp":
		return TransportTailscaleRelay
	}
	return TransportUnknown
}

func (s Session) payload() map[string]string {
	mode := s.Mode
	if mode != ModeClient && mode != ModeHost {
		mode = unknown
	}
	return map[string]string{"mode": mode, "codec": CodecName(s.Codec), "transportKind": TransportKind(s.Transport)}
}

// Signal is one TelemetryDeck v2 event.
type Signal struct {
	AppID      string            `json:"appID"`
	ClientUser string            `json:"clientUser"`
	SessionID  string            `json:"sessionID"`
	Type       string            `json:"type"`
	IsTestMode bool              `json:"isTestMode"`
	Payload    map[string]string `json:"payload"`
}

// state is <config>/telemetry.json.
type state struct {
	InstallID       string `json:"install_id"`
	FirstLaunchSent bool   `json:"first_launch_sent"`
}

// Client queues signals and sends them on its own goroutine. A nil Client
// sends nothing.
type Client struct {
	o         Options
	sessionID string

	mu       sync.Mutex
	enabled  bool
	launched bool
	st       state
	queue    []Signal
	// gen changes when telemetry is turned off, so a send that started
	// before does not put its signals back for a retry.
	gen    int
	cancel context.CancelFunc

	wake chan struct{}
	stop chan struct{}
	done chan struct{}
}

// New starts a client; enabled is the user's setting.
func New(o Options, enabled bool) *Client {
	if o.HTTP == nil {
		o.HTTP = &http.Client{Timeout: 15 * time.Second}
	}
	if o.RetryAfter == 0 {
		o.RetryAfter = time.Minute
	}
	c := &Client{o: o, sessionID: newUUID(), enabled: enabled,
		wake: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{})}
	c.st = loadState(o.StateFile)
	go c.loop()
	return c
}

// Active is true when signals can leave: an App ID is set and the user has
// telemetry on.
func (c *Client) Active() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.activeLocked()
}

func (c *Client) activeLocked() bool { return c.enabled && strings.TrimSpace(c.o.AppID) != "" }

// SetEnabled follows the user's setting. Off drops the queue and cancels a
// send in flight.
func (c *Client) SetEnabled(on bool) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.enabled == on {
		return
	}
	c.enabled = on
	if !on {
		c.queue = nil
		c.gen++
		if c.cancel != nil {
			c.cancel()
		}
	}
}

// Launched sends app.launched, after app.installed the first time on this
// install. It sends once per run; while telemetry is off it waits for the
// next call.
func (c *Client) Launched() {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.launched || !c.activeLocked() {
		c.mu.Unlock()
		return
	}
	c.launched = true
	if !c.st.FirstLaunchSent {
		c.enqueueLocked(TypeInstalled, nil)
	}
	c.enqueueLocked(TypeLaunched, nil)
	c.mu.Unlock()
	c.poke()
}

// SessionStarted sends session.started.
func (c *Client) SessionStarted(s Session) { c.send(TypeSessionStarted, s.payload()) }

// SessionEnded sends session.ended.
func (c *Client) SessionEnded(s Session) { c.send(TypeSessionEnded, s.payload()) }

func (c *Client) send(typ string, extra map[string]string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	if !c.activeLocked() {
		c.mu.Unlock()
		return
	}
	c.enqueueLocked(typ, extra)
	c.mu.Unlock()
	c.poke()
}

// Close stops the client, giving a queued send up to wait to finish.
func (c *Client) Close(wait time.Duration) {
	if c == nil {
		return
	}
	select {
	case <-c.stop:
		return
	default:
	}
	close(c.stop)
	select {
	case <-c.done:
	case <-time.After(wait):
		c.mu.Lock()
		if c.cancel != nil {
			c.cancel()
		}
		c.mu.Unlock()
	}
}

func (c *Client) enqueueLocked(typ string, extra map[string]string) {
	if c.st.InstallID == "" {
		c.st.InstallID = newUUID()
		c.saveStateLocked()
	}
	payload := map[string]string{"os": runtime.GOOS, "arch": runtime.GOARCH, "appVersion": c.o.AppVersion,
		"buildChannel": c.o.Channel, "platform": platformTag, "mode": c.o.Mode}
	for k, v := range extra {
		payload[k] = v
	}
	c.queue = append(c.queue, Signal{AppID: c.o.AppID, ClientUser: ClientUser(c.st.InstallID), SessionID: c.sessionID,
		Type: typ, IsTestMode: c.o.TestMode, Payload: payload})
	if n := len(c.queue); n > maxQueued {
		c.queue = c.queue[n-maxQueued:]
	}
}

func (c *Client) poke() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (c *Client) loop() {
	defer close(c.done)
	var retry <-chan time.Time
	for {
		select {
		case <-c.stop:
			c.flush()
			return
		case <-c.wake:
		case <-retry:
		}
		retry = nil
		if !c.flush() {
			retry = time.After(c.o.RetryAfter)
		}
	}
}

// flush sends the queue in one request. False: it failed and the signals
// wait for a retry.
func (c *Client) flush() bool {
	c.mu.Lock()
	if len(c.queue) == 0 || !c.activeLocked() {
		c.queue = nil
		c.mu.Unlock()
		return true
	}
	batch, gen := c.queue, c.gen
	c.queue = nil
	// app.installed is marked sent before it leaves: a send cut off by off
	// or a timeout may still have reached the server, and a second one would
	// count the install twice.
	marked := false
	if !c.st.FirstLaunchSent && hasType(batch, TypeInstalled) {
		c.st.FirstLaunchSent, marked = true, true
		c.saveStateLocked()
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	c.mu.Unlock()

	err := c.post(ctx, batch)
	cancel()

	c.mu.Lock()
	defer c.mu.Unlock()
	c.cancel = nil
	var refused statusError
	if marked && errors.As(err, &refused) {
		c.st.FirstLaunchSent = false
		c.saveStateLocked()
	}
	if gen != c.gen || !c.activeLocked() {
		return true // turned off meanwhile: nothing goes back
	}
	if err != nil {
		slog.Debug("telemetry: send failed", "err", err)
		c.queue = append(batch, c.queue...)
		if n := len(c.queue); n > maxQueued {
			c.queue = c.queue[n-maxQueued:]
		}
		return false
	}
	return true
}

func (c *Client) post(ctx context.Context, batch []Signal) error {
	body, err := json.Marshal(batch)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.o.Endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := c.o.HTTP.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return statusError(resp.StatusCode)
	}
	return nil
}

// statusError is a non-2xx answer: the server did not take the batch.
type statusError int

func (e statusError) Error() string { return fmt.Sprintf("status %d", int(e)) }

func hasType(batch []Signal, typ string) bool {
	for _, s := range batch {
		if s.Type == typ {
			return true
		}
	}
	return false
}

func (c *Client) saveStateLocked() {
	if c.o.StateFile == "" {
		return
	}
	data, err := json.MarshalIndent(c.st, "", "  ")
	if err == nil {
		err = os.MkdirAll(filepath.Dir(c.o.StateFile), 0o700)
	}
	if err == nil {
		err = writeAtomic(c.o.StateFile, data)
	}
	if err != nil {
		slog.Warn("telemetry: could not save state", "err", err)
	}
}

// writeAtomic writes through a temp file of its own and a rename, so the
// client and the host saving at once never leave a half-written file.
func writeAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	_, err = f.Write(data)
	if err == nil {
		err = f.Chmod(0o600)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func loadState(path string) state {
	var s state
	if path == "" {
		return s
	}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &s)
	}
	return s
}

// ClientUser is the clientUser of an install: the lowercase hex SHA-256 of
// its random ID. TelemetryDeck hashes it again, with a salt.
func ClientUser(installID string) string {
	sum := sha256.Sum256([]byte(installID))
	return hex.EncodeToString(sum[:])
}

// newUUID is a random (version 4) UUID.
func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
