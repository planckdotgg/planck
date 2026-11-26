package winres

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/planckdotgg/planck/internal/version"
)

var root = filepath.Join("..", "..")

func icon() string { return filepath.Join(root, "assets", "planck-windows.ico") }

// The committed .syso files are the output of generate for the committed
// icon and version, so a version bump or a new icon cannot ship with the
// old resources.
func TestCommittedResourcesMatch(t *testing.T) {
	for _, tg := range Targets {
		want, err := Generate(icon(), tg.Exe, version.Version)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, filepath.FromSlash(tg.Dir), SysoName)
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%v: run go run ./scripts/winres generate", err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s/%s is stale: run go run ./scripts/winres generate", tg.Dir, SysoName)
		}
	}
}

func TestVersionInfoRoundTrip(t *testing.T) {
	want := Exe{Name: "planck-host", Description: "Planck Host"}.info("1.2.3-rc.1")
	b, err := versionInfo(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := parseVersionInfo(b)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != "1.2.3" {
		t.Errorf("fixed version %q, want 1.2.3", got.Version)
	}
	for k, v := range want.Strings {
		if got.Strings[k] != v {
			t.Errorf("%s = %q, want %q", k, got.Strings[k], v)
		}
	}
}

func TestVersionNumbersRejectsBadVersions(t *testing.T) {
	for _, v := range []string{"1.2", "1.2.x", "1.2.3.4", "70000.0.0"} {
		if _, err := versionNumbers(v); err == nil {
			t.Errorf("%q accepted", v)
		}
	}
}

// A Windows build of each command links its resources, and Check finds
// every icon size and the version.
func TestWindowsExesHaveResources(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the commands")
	}
	for _, tg := range Targets {
		exe := filepath.Join(t.TempDir(), tg.Exe.Name+".exe")
		cmd := exec.Command("go", "build", "-tags", "mygo_noinspector", "-ldflags=-s -w", "-o", exe, "./"+tg.Dir)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GOOS=windows", "GOARCH=amd64", "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v\n%s", tg.Dir, err, out)
		}
		sizes, info, err := Check(exe)
		if err != nil {
			t.Fatalf("%s: %v", tg.Dir, err)
		}
		if len(sizes) < len(IconSizes) {
			t.Errorf("%s: sizes %v", tg.Dir, sizes)
		}
		if info.Strings["ProductVersion"] != version.Version || info.Strings["OriginalFilename"] != tg.Exe.Name+".exe" {
			t.Errorf("%s: version strings %v", tg.Dir, info.Strings)
		}
	}
}

func TestCheckRejectsAnExeWithoutResources(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a program")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, "x.exe")
	cmd := exec.Command("go", "build", "-o", exe, ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOOS=windows", "GOARCH=amd64", "CGO_ENABLED=0", "GOFLAGS=")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	if _, _, err := Check(exe); err == nil {
		t.Error("check passed an exe with no resources")
	}
}

func TestIconImagesNeedsEverySize(t *testing.T) {
	_, sizes, err := iconImages(mustRead(t, icon()))
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(sizes); i++ {
		if sizes[i] <= sizes[i-1] {
			t.Fatalf("sizes not ascending: %v", sizes)
		}
	}
	fav := mustRead(t, filepath.Join(root, "assets", "favicon.ico"))
	if _, _, err := iconImages(fav); err == nil {
		t.Error("favicon.ico (16-48 px) accepted as the app icon")
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
