//go:build linux && !android && (amd64 || arm64)

package pipewire

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/godbus/dbus/v5"

	"github.com/planckdotgg/planck/internal/linux/portal"
)

const screenCast = "org.freedesktop.portal.ScreenCast"

// ScreenCast source types and cursor modes.
const (
	sourceMonitor       = 1
	cursorHidden        = 1
	cursorEmbedded      = 2
	persistUntilRevoked = 2
)

// ErrNoPortal means no desktop portal offers ScreenCast on the session
// bus.
var ErrNoPortal = errors.New("pipewire: no desktop portal with ScreenCast")

// castSession is a started ScreenCast: the PipeWire node of the monitor
// the user picked and a PipeWire connection that may read it.
type castSession struct {
	conn    *dbus.Conn
	session dbus.ObjectPath
	node    uint32
	fd      int
	x, y    int
	w, h    int
}

// portalVersion is the ScreenCast interface's version, ErrNoPortal
// without one.
func portalVersion(conn *dbus.Conn) (uint32, error) {
	v, err := conn.Object(portal.Name, portal.Path).GetProperty(screenCast + ".version")
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrNoPortal, err)
	}
	n, _ := v.Value().(uint32)
	return n, nil
}

// Available reports whether the session bus has a ScreenCast portal.
func Available() error {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNoPortal, err)
	}
	defer conn.Close()
	_, err = portalVersion(conn)
	return err
}

// tokenPath keeps the portal's restore token, so the user picks a
// monitor once and later sessions start without asking.
func tokenPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "planck", "screencast-restore-token")
}

// startCast asks the portal for one monitor, the cursor drawn in, and
// opens the PipeWire remote that streams it.
func startCast() (*castSession, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoPortal, err)
	}
	c := &castSession{conn: conn, fd: -1}
	if err := c.start(); err != nil {
		c.close()
		return nil, err
	}
	return c, nil
}

func (c *castSession) start() error {
	version, err := portalVersion(c.conn)
	if err != nil {
		return err
	}
	res, err := portal.Request(c.conn, screenCast, "CreateSession", nil,
		map[string]dbus.Variant{"session_handle_token": dbus.MakeVariant(portal.Token())})
	if err != nil {
		return err
	}
	if c.session, err = portal.SessionHandle(res); err != nil {
		return err
	}
	cursor := uint32(cursorHidden)
	if v, err := c.conn.Object(portal.Name, portal.Path).GetProperty(screenCast + ".AvailableCursorModes"); err == nil {
		if m, _ := v.Value().(uint32); m&cursorEmbedded != 0 {
			cursor = cursorEmbedded
		}
	}
	opts := map[string]dbus.Variant{
		"types":       dbus.MakeVariant(uint32(sourceMonitor)),
		"multiple":    dbus.MakeVariant(false),
		"cursor_mode": dbus.MakeVariant(cursor),
	}
	path := tokenPath()
	if version >= 4 {
		opts["persist_mode"] = dbus.MakeVariant(uint32(persistUntilRevoked))
		if b, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(b))) > 0 {
			opts["restore_token"] = dbus.MakeVariant(strings.TrimSpace(string(b)))
		}
	}
	if _, err := portal.Request(c.conn, screenCast, "SelectSources", []any{c.session}, opts); err != nil {
		return err
	}
	res, err = portal.Request(c.conn, screenCast, "Start", []any{c.session, ""}, map[string]dbus.Variant{})
	if err != nil {
		return err
	}
	if t, ok := res["restore_token"].Value().(string); ok && t != "" && path != "" {
		if os.MkdirAll(filepath.Dir(path), 0o700) == nil {
			_ = os.WriteFile(path, []byte(t), 0o600)
		}
	}
	if err := c.readStream(res["streams"]); err != nil {
		return err
	}
	var fd dbus.UnixFD
	if err := c.conn.Object(portal.Name, portal.Path).Call(screenCast+".OpenPipeWireRemote", 0, c.session, map[string]dbus.Variant{}).Store(&fd); err != nil {
		return fmt.Errorf("OpenPipeWireRemote: %w", err)
	}
	c.fd = int(fd)
	return nil
}

// readStream takes the first of Start's streams, a(ua{sv}): its node,
// and the place and size the portal gives for it.
func (c *castSession) readStream(v dbus.Variant) error {
	streams, ok := v.Value().([][]any)
	if !ok || len(streams) == 0 || len(streams[0]) < 2 {
		return errors.New("pipewire: the portal started no stream")
	}
	c.node, _ = streams[0][0].(uint32)
	props, _ := streams[0][1].(map[string]dbus.Variant)
	pair := func(key string) (int, int) {
		if p, ok := props[key].Value().([]any); ok && len(p) == 2 {
			a, _ := p[0].(int32)
			b, _ := p[1].(int32)
			return int(a), int(b)
		}
		return 0, 0
	}
	c.x, c.y = pair("position")
	c.w, c.h = pair("size")
	return nil
}

func (c *castSession) close() {
	if c.session != "" {
		_ = portal.CloseSession(c.conn, c.session)
		c.session = ""
	}
	if c.conn != nil {
		c.conn.Close()
		c.conn = nil
	}
}
