// Package portal calls XDG desktop portal methods that answer through a
// Request object, on the session bus.
package portal

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	Name     = "org.freedesktop.portal.Desktop"
	Path     = "/org/freedesktop/portal/desktop"
	request  = "org.freedesktop.portal.Request"
	response = request + ".Response"
	session  = "org.freedesktop.portal.Session"
	// timeout covers a person answering the portal's dialog.
	timeout = 2 * time.Minute
)

// ErrDeclined is a request the user declined or the portal cancelled.
var ErrDeclined = errors.New("portal: the user declined, or the request was cancelled")

// Token is a fresh handle_token or session_handle_token.
func Token() string {
	var r [8]byte
	_, _ = rand.Read(r[:])
	return "planck" + hex.EncodeToString(r[:])
}

// RequestPath is the object path a call with handle_token token answers
// on, for a caller whose unique bus name is sender.
func RequestPath(sender, token string) dbus.ObjectPath {
	return dbus.ObjectPath("/org/freedesktop/portal/desktop/request/" +
		strings.ReplaceAll(strings.TrimPrefix(sender, ":"), ".", "_") + "/" + token)
}

// Request calls iface.method on the portal with lead arguments, then
// opts with a handle_token added, and waits for its Response's results.
func Request(conn *dbus.Conn, iface, method string, lead []any, opts map[string]dbus.Variant) (map[string]dbus.Variant, error) {
	token := Token()
	opts["handle_token"] = dbus.MakeVariant(token)
	path := RequestPath(conn.Names()[0], token)
	sig := make(chan *dbus.Signal, 4)
	conn.Signal(sig)
	defer conn.RemoveSignal(sig)
	match := []dbus.MatchOption{dbus.WithMatchObjectPath(path), dbus.WithMatchInterface(request), dbus.WithMatchMember("Response")}
	if err := conn.AddMatchSignal(match...); err != nil {
		return nil, err
	}
	defer conn.RemoveMatchSignal(match...)
	var handle dbus.ObjectPath
	if err := conn.Object(Name, Path).Call(iface+"."+method, 0, append(lead, opts)...).Store(&handle); err != nil {
		return nil, fmt.Errorf("%s: %w", method, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	for {
		select {
		case s := <-sig:
			if s.Name != response || s.Path != handle || len(s.Body) < 2 {
				continue
			}
			code, _ := s.Body[0].(uint32)
			res, _ := s.Body[1].(map[string]dbus.Variant)
			if code != 0 {
				return nil, fmt.Errorf("%s: %w (response %d)", method, ErrDeclined, code)
			}
			return res, nil
		case <-ctx.Done():
			return nil, fmt.Errorf("%s: no portal response: %w", method, ctx.Err())
		}
	}
}

// SessionHandle reads the session_handle of a CreateSession response.
func SessionHandle(res map[string]dbus.Variant) (dbus.ObjectPath, error) {
	switch h := res["session_handle"].Value().(type) {
	case string:
		return dbus.ObjectPath(h), nil
	case dbus.ObjectPath:
		return h, nil
	}
	return "", errors.New("portal: CreateSession returned no session_handle")
}

// CloseSession ends a portal session.
func CloseSession(conn *dbus.Conn, handle dbus.ObjectPath) error {
	return conn.Object(Name, handle).Call(session+".Close", 0).Err
}
