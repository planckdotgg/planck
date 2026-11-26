//go:build linux && !android && (amd64 || arm64)

package display

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConnectedMode(t *testing.T) {
	dir := t.TempDir()
	conn := func(name, status, modes string) {
		p := filepath.Join(dir, name)
		os.MkdirAll(p, 0o755)
		os.WriteFile(filepath.Join(p, "status"), []byte(status+"\n"), 0o644)
		os.WriteFile(filepath.Join(p, "modes"), []byte(modes), 0o644)
	}
	conn("card0-DP-1", "disconnected", "")
	conn("card0-DP-2", "connected", "2560x1440\n1920x1080\n")
	conn("card0-HDMI-A-1", "connected", "3840x2160\n")
	if w, h, ok := connectedMode(dir); !ok || w != 2560 || h != 1440 {
		t.Fatalf("%dx%d %v", w, h, ok)
	}
	if _, _, ok := connectedMode(t.TempDir()); ok {
		t.Fatal("a mode with no connector")
	}
}

func TestBackendFromSession(t *testing.T) {
	for _, c := range []struct{ forced, session, wayland, x11, want string }{
		{"", "wayland", "wayland-0", ":0", BackendPortal},
		{"", "x11", "", ":0", BackendX11},
		{"", "", "wayland-0", ":0", BackendPortal},
		{"x11", "wayland", "wayland-0", ":0", BackendX11},
		{"", "tty", "", "", ""},
	} {
		t.Setenv("PLANCK_CAPTURE_BACKEND", c.forced)
		t.Setenv("XDG_SESSION_TYPE", c.session)
		t.Setenv("WAYLAND_DISPLAY", c.wayland)
		t.Setenv("DISPLAY", c.x11)
		if got := Backend(); got != c.want {
			t.Errorf("%+v: %q", c, got)
		}
	}
}
