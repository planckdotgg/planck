package input

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	portalName     = "org.freedesktop.portal.Desktop"
	portalPath     = "/org/freedesktop/portal/desktop"
	portalRD       = "org.freedesktop.portal.RemoteDesktop"
	portalRequest  = "org.freedesktop.portal.Request"
	portalResponse = portalRequest + ".Response"
	portalTimeout  = 2 * time.Minute
)

// portalBackend injects through the RemoteDesktop portal, the sanctioned
// path on Wayland when /dev/uinput is closed to the user. Starting a
// session shows the compositor's consent dialog. Absolute moves need a
// ScreenCast stream to name, which this backend does not hold, so only
// relative moves work.
type portalBackend struct {
	conn    *dbus.Conn
	obj     dbus.BusObject
	session dbus.ObjectPath

	mu         sync.Mutex
	accX, accY wheelAccum
}

func openPortalBackend() (*portalBackend, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, fmt.Errorf("session bus: %w", err)
	}
	b := &portalBackend{conn: conn, obj: conn.Object(portalName, portalPath)}
	if err := b.start(); err != nil {
		conn.Close()
		return nil, err
	}
	return b, nil
}

func newToken() string {
	var r [8]byte
	_, _ = rand.Read(r[:])
	return "planck" + hex.EncodeToString(r[:])
}

// request calls a portal method that answers through a Request object and
// waits for its Response. args come between the method's leading
// arguments and its options; opts gets a handle_token added.
func (b *portalBackend) request(method string, lead []any, opts map[string]dbus.Variant) (map[string]dbus.Variant, error) {
	token := newToken()
	opts["handle_token"] = dbus.MakeVariant(token)
	path := dbus.ObjectPath(portalRequestPath(b.conn.Names()[0], token))
	sig := make(chan *dbus.Signal, 4)
	b.conn.Signal(sig)
	defer b.conn.RemoveSignal(sig)
	match := []dbus.MatchOption{dbus.WithMatchObjectPath(path), dbus.WithMatchInterface(portalRequest), dbus.WithMatchMember("Response")}
	if err := b.conn.AddMatchSignal(match...); err != nil {
		return nil, err
	}
	defer b.conn.RemoveMatchSignal(match...)
	var handle dbus.ObjectPath
	if err := b.obj.Call(portalRD+"."+method, 0, append(lead, opts)...).Store(&handle); err != nil {
		return nil, fmt.Errorf("%s: %w", method, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), portalTimeout)
	defer cancel()
	for {
		select {
		case s := <-sig:
			if s.Name != portalResponse || s.Path != handle || len(s.Body) < 2 {
				continue
			}
			code, _ := s.Body[0].(uint32)
			res, _ := s.Body[1].(map[string]dbus.Variant)
			if code != 0 {
				return nil, fmt.Errorf("%s: portal response %d (the user declined, or the request was cancelled)", method, code)
			}
			return res, nil
		case <-ctx.Done():
			return nil, fmt.Errorf("%s: no portal response: %w", method, ctx.Err())
		}
	}
}

func (b *portalBackend) start() error {
	res, err := b.request("CreateSession", nil, map[string]dbus.Variant{"session_handle_token": dbus.MakeVariant(newToken())})
	if err != nil {
		return err
	}
	switch h := res["session_handle"].Value().(type) {
	case string:
		b.session = dbus.ObjectPath(h)
	case dbus.ObjectPath:
		b.session = h
	default:
		return errors.New("CreateSession returned no session_handle")
	}
	if _, err := b.request("SelectDevices", []any{b.session},
		map[string]dbus.Variant{"types": dbus.MakeVariant(uint32(portalKeyboard | portalPointer))}); err != nil {
		return err
	}
	res, err = b.request("Start", []any{b.session, ""}, map[string]dbus.Variant{})
	if err != nil {
		return err
	}
	if got, _ := res["devices"].Value().(uint32); got&(portalKeyboard|portalPointer) == 0 {
		return errors.New("the portal granted no keyboard or pointer")
	}
	return nil
}

func (b *portalBackend) notify(method string, args ...any) error {
	call := b.obj.Call(portalRD+"."+method, 0, append([]any{b.session, map[string]dbus.Variant{}}, args...)...)
	return call.Err
}

func (b *portalBackend) key(code uint16, down bool) error {
	return b.notify("NotifyKeyboardKeycode", int32(code), uint32(boolByte(down)))
}

func (b *portalBackend) moveRel(dx, dy int32) error {
	return b.notify("NotifyPointerMotion", float64(dx), float64(dy))
}

func (b *portalBackend) moveAbs(MouseMoveAbs, Rect, Rect) error {
	return fmt.Errorf("%w: the RemoteDesktop portal needs a ScreenCast stream for absolute moves", ErrUnsupported)
}

func (b *portalBackend) button(code uint16, down bool) error {
	return b.notify("NotifyPointerButton", int32(code), uint32(boolByte(down)))
}

func (b *portalBackend) wheel(w MouseWheel) error {
	b.mu.Lock()
	dy, dx := portalSteps(w.DY, &b.accY), -portalSteps(w.DX, &b.accX)
	b.mu.Unlock()
	if dy != 0 {
		if err := b.notify("NotifyPointerAxisDiscrete", uint32(portalAxisVertical), dy); err != nil {
			return err
		}
	}
	if dx != 0 {
		return b.notify("NotifyPointerAxisDiscrete", uint32(portalAxisHorizontal), dx)
	}
	return nil
}

func (b *portalBackend) close() error {
	_ = b.conn.Object(portalName, b.session).Call("org.freedesktop.portal.Session.Close", 0).Err
	return b.conn.Close()
}
