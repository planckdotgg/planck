package config

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestHostname(t *testing.T) {
	n := Hostname()
	if n == "" || strings.HasSuffix(n, ".local") || strings.TrimSpace(n) != n {
		t.Fatalf("Hostname() = %q", n)
	}
	if runtime.GOOS == "darwin" {
		out, err := exec.Command("/usr/sbin/scutil", "--get", "ComputerName").Output()
		if err != nil {
			t.Skip("scutil:", err)
		}
		if want := strings.TrimSpace(string(out)); n != want {
			t.Fatalf("Hostname() = %q, want the Computer Name %q", n, want)
		}
		return
	}
	if runtime.GOOS == "linux" {
		if b, err := os.ReadFile("/etc/machine-info"); err == nil {
			if want := prettyHostname(b); want != "" {
				if n != want {
					t.Fatalf("Hostname() = %q, want PRETTY_HOSTNAME %q", n, want)
				}
				return
			}
		}
	}
	if h, _ := os.Hostname(); n != strings.TrimSuffix(h, ".local") {
		t.Fatalf("Hostname() = %q, os.Hostname() = %q", n, h)
	}
}

func TestDefaultHostConfigName(t *testing.T) {
	if got := DefaultHostConfig(t.TempDir()).Name; got != Hostname() {
		t.Fatalf("default name %q, want %q", got, Hostname())
	}
}

// TestUpdateClientConfigSerializes: concurrent updates of different keys
// all land, and aliases and route addresses survive a round trip.
func TestUpdateClientConfigSerializes(t *testing.T) {
	dir := t.TempDir()
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			err := UpdateClientConfig(dir, func(c *ClientConfig) error {
				if c.HostAliases == nil {
					c.HostAliases = map[string]string{}
				}
				c.HostAliases[string(rune('a'+i))] = "x"
				return nil
			})
			if err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	err := UpdateClientConfig(dir, func(c *ClientConfig) error {
		return c.Pin(PinnedHost{HostID: "h", CertFingerprint: strings.Repeat("ab", 32), LastAddr: "192.168.1.2:47701",
			LastLANAddr: "192.168.1.2:47701", LastTailscaleAddr: "100.64.0.2:47701"})
	})
	if err != nil {
		t.Fatal(err)
	}
	c, err := LoadClientConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.HostAliases) != 20 {
		t.Fatalf("%d aliases survived, want 20", len(c.HostAliases))
	}
	if p := c.PinnedHosts["h"]; p.LastLANAddr != "192.168.1.2:47701" || p.LastTailscaleAddr != "100.64.0.2:47701" {
		t.Fatalf("pin %+v", p)
	}
}

func TestPrettyHostname(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"CHASSIS=desktop\nPRETTY_HOSTNAME=\"Chris's Desktop\"\n", "Chris's Desktop"},
		{"PRETTY_HOSTNAME='Living Room PC'\n", "Living Room PC"},
		{"PRETTY_HOSTNAME=plain\n", "plain"},
		{"  PRETTY_HOSTNAME = \"spaced\"  \n", "spaced"},
		{"PRETTY_HOSTNAME=\"say \\\"hi\\\"\"\n", `say "hi"`},
		{"# PRETTY_HOSTNAME=commented\nICON_NAME=computer\n", ""},
		{"PRETTY_HOSTNAME=\"\"\n", ""},
		{"OTHER_PRETTY_HOSTNAME=x\n", ""},
	}
	for _, c := range cases {
		if got := prettyHostname([]byte(c.in)); got != c.want {
			t.Errorf("prettyHostname(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
