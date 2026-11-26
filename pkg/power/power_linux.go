package power

import (
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/godbus/dbus/v5"
)

const (
	loginName    = "org.freedesktop.login1"
	loginPath    = "/org/freedesktop/login1"
	screenSaver  = "org.freedesktop.ScreenSaver"
	appName      = "Planck"
	inhibitKinds = "idle:sleep"
)

// KeepAwake stops the screen saver and idle suspend until release, through
// the session's org.freedesktop.ScreenSaver service (GNOME, KDE and most
// others) and a logind idle:sleep inhibitor lock. It succeeds when either
// takes hold.
func KeepAwake(reason string) (release func(), err error) {
	var releases []func()
	var errs []error

	if conn, err := dbus.ConnectSessionBus(); err != nil {
		errs = append(errs, fmt.Errorf("session bus: %w", err))
	} else if cookie, err := screenSaverInhibit(conn, reason); err != nil {
		errs = append(errs, err)
		conn.Close()
	} else {
		releases = append(releases, func() {
			unInhibitScreenSaver(conn, cookie)
			conn.Close()
		})
	}

	if conn, err := dbus.ConnectSystemBus(); err != nil {
		errs = append(errs, fmt.Errorf("system bus: %w", err))
	} else if lock, err := loginInhibit(conn, reason); err != nil {
		errs = append(errs, err)
		conn.Close()
	} else {
		releases = append(releases, func() {
			lock.Close()
			conn.Close()
		})
	}

	if len(releases) == 0 {
		return nil, fmt.Errorf("power: nothing took the inhibitor: %w", errors.Join(errs...))
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			for _, r := range releases {
				r()
			}
		})
	}, nil
}

// screenSaverPaths are where the ScreenSaver service answers: the
// freedesktop name and the older KDE one.
var screenSaverPaths = []dbus.ObjectPath{"/org/freedesktop/ScreenSaver", "/ScreenSaver"}

func screenSaverInhibit(conn *dbus.Conn, reason string) (uint32, error) {
	var errs []error
	for _, path := range screenSaverPaths {
		var cookie uint32
		err := conn.Object(screenSaver, path).Call(screenSaver+".Inhibit", 0, appName, reason).Store(&cookie)
		if err == nil {
			return cookie, nil
		}
		errs = append(errs, err)
	}
	return 0, fmt.Errorf("ScreenSaver.Inhibit: %w", errors.Join(errs...))
}

func unInhibitScreenSaver(conn *dbus.Conn, cookie uint32) {
	for _, path := range screenSaverPaths {
		if conn.Object(screenSaver, path).Call(screenSaver+".UnInhibit", 0, cookie).Err == nil {
			return
		}
	}
}

// loginInhibit takes a logind lock; it holds until the returned file, the
// read end of a pipe logind keeps, is closed.
func loginInhibit(conn *dbus.Conn, reason string) (*os.File, error) {
	var fd dbus.UnixFD
	err := conn.Object(loginName, loginPath).Call(loginName+".Manager.Inhibit", 0, inhibitKinds, appName, reason, "block").Store(&fd)
	if err != nil {
		return nil, fmt.Errorf("login1 Inhibit: %w", err)
	}
	return os.NewFile(uintptr(fd), "logind-inhibitor"), nil
}

// LockScreen locks this process's login session, as loginctl lock-session
// does; the desktop's screen locker answers the signal.
func LockScreen() error {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return fmt.Errorf("power: system bus: %w", err)
	}
	defer conn.Close()
	session := conn.Object(loginName, "/org/freedesktop/login1/session/auto")
	if err := session.Call(loginName+".Session.Lock", 0).Err; err != nil {
		return fmt.Errorf("power: lock the session: %w", err)
	}
	return nil
}
