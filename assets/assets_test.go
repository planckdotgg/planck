package assets

import (
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
