package main

import "testing"

func TestInAppBundle(t *testing.T) {
	for exe, want := range map[string]bool{
		"/Applications/Planck.app/Contents/MacOS/planck-client": true,
		"/Users/a/Desktop/Planck.app/Contents/MacOS/Planck":     true,
		"/tmp/go-build123/b001/exe/planck-client":               false,
		"/Users/a/go/bin/planck-client":                         false,
		"/Users/a/Planck.app/planck-client":                     false,
		"":                                                      false,
	} {
		if got := inAppBundle(exe); got != want {
			t.Errorf("inAppBundle(%q) = %v, want %v", exe, got, want)
		}
	}
}
