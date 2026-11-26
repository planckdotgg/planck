package version

import (
	"os"
	"regexp"
	"testing"
)

func TestFull(t *testing.T) {
	defer func(v, c, d string) { Version, Commit, BuildDate = v, c, d }(Version, Commit, BuildDate)
	Version, Commit, BuildDate = "1.2.3", "dev", ""
	if got := Full(); got != "1.2.3 (dev)" {
		t.Errorf("Full() = %q", got)
	}
	Commit, BuildDate = "abc1234", "2026-11-25T00:00:00Z"
	if got := Full(); got != "1.2.3 (abc1234, 2026-11-25T00:00:00Z)" {
		t.Errorf("Full() = %q", got)
	}
}

// scripts/version.sh and bundle-windows.ps1 read the version with a
// line pattern, so the declaration must keep this exact form.
func TestDeclarationReadableByScripts(t *testing.T) {
	src, err := os.ReadFile("version.go")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^var Version = "([0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?)"$`).FindSubmatch(src)
	if m == nil || string(m[1]) != Version {
		t.Fatalf("version.go has no line var Version = \"X.Y.Z\" matching Version %q", Version)
	}
}
