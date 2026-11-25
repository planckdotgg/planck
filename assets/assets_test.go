package assets

import (
	"bytes"
	"encoding/binary"
	"image/png"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEmbeddedFonts(t *testing.T) {
	fonts, err := fs.Glob(FS, "fonts/*.ttf")
	if err != nil {
		t.Fatal(err)
	}
	if len(fonts) != 6 {
		t.Fatalf("embedded fonts = %q, want the 6 static instances", fonts)
	}
	for _, f := range fonts {
		data, _ := FS.ReadFile(f)
		// TrueType outlines start with version 1.0.
		if len(data) < 4 || string(data[:4]) != "\x00\x01\x00\x00" {
			t.Errorf("%s is not a TrueType font", f)
		}
	}
	for _, lic := range []string{"fonts/OFL-Archivo.txt", "fonts/OFL-MartianMono.txt"} {
		data, err := os.ReadFile(lic)
		if err != nil || !strings.Contains(string(data), "SIL OPEN FONT LICENSE") {
			t.Errorf("%s missing or not the OFL: %v", lic, err)
		}
	}
}

// TestIconsPathOnly keeps every shipped SVG to paths and shapes: no text
// that depends on installed fonts, no linked or embedded images.
func TestIconsPathOnly(t *testing.T) {
	files, err := filepath.Glob("icons/*.svg")
	if err != nil || len(files) == 0 {
		t.Fatalf("no icons: %v", err)
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		s := string(data)
		if strings.Contains(s, "<text") || strings.Contains(s, "<image") || strings.Contains(s, "href=") {
			t.Errorf("%s is not path-only", f)
		}
	}
}

func TestAppIconsArePNG(t *testing.T) {
	for name, want := range map[string]int{"darwin": 1024, "windows": 256, "linux": 256} {
		cfg, err := png.DecodeConfig(bytes.NewReader(AppIcon(name)))
		if err != nil {
			t.Fatalf("%s icon is not a PNG: %v", name, err)
		}
		if cfg.Width != want || cfg.Height != want {
			t.Errorf("%s icon is %dx%d, want %d square", name, cfg.Width, cfg.Height, want)
		}
	}
}

func TestAppIconICNS(t *testing.T) {
	data, err := os.ReadFile("AppIcon.icns")
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < 8 || string(data[:4]) != "icns" {
		t.Fatalf("AppIcon.icns does not start with the icns magic")
	}
	if got := binary.BigEndian.Uint32(data[4:8]); int(got) != len(data) {
		t.Errorf("AppIcon.icns header length %d, file is %d bytes", got, len(data))
	}
}

func TestWindowsICOSizes(t *testing.T) {
	data, err := os.ReadFile("planck-windows.ico")
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < 6 || binary.LittleEndian.Uint16(data[2:4]) != 1 {
		t.Fatal("planck-windows.ico is not an ICO")
	}
	n := int(binary.LittleEndian.Uint16(data[4:6]))
	var got []int
	for i := 0; i < n; i++ {
		w := int(data[6+16*i])
		if w == 0 {
			w = 256
		}
		got = append(got, w)
	}
	want := []int{16, 20, 24, 32, 40, 48, 64, 96, 128, 256}
	if len(got) != len(want) {
		t.Fatalf("ico sizes = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ico sizes = %v, want %v", got, want)
		}
	}
}
