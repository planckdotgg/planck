package tailscale

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMacsysEndpoint(t *testing.T) {
	dir := shortTempDir(t)
	if err := os.Symlink("56236", filepath.Join(dir, "ipnport")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sameuserproof-56236"), []byte("abc123\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	ep, err := macsysEndpoint(dir)
	if err != nil {
		t.Fatal(err)
	}
	if want := (endpoint{network: "tcp", address: "127.0.0.1:56236", token: "abc123"}); ep != want {
		t.Fatalf("endpoint = %+v, want %+v", ep, want)
	}

	if _, err := macsysEndpoint(shortTempDir(t)); err == nil {
		t.Fatal("empty dir resolved an endpoint")
	}
}

func TestParseAppStoreLsof(t *testing.T) {
	out := []byte("p812\ncIPNExtension\nf3\nn/dev/null\nf9\nn/Users/me/Library/Group Containers/io.tailscale.ipn.macos/sameuserproof-61234-deadbeef\n")
	ep, err := parseAppStoreLsof(out)
	if err != nil {
		t.Fatal(err)
	}
	if want := (endpoint{network: "tcp", address: "127.0.0.1:61234", token: "deadbeef"}); ep != want {
		t.Fatalf("endpoint = %+v, want %+v", ep, want)
	}
	if _, err := parseAppStoreLsof([]byte("p1\nn/dev/null\n")); err == nil {
		t.Fatal("parsed an endpoint from lsof output without a sameuserproof file")
	}
}
