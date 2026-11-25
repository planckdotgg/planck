package main

import "strings"

// inAppBundle reports whether exe is the main executable of a macOS .app
// bundle, which takes its icon from Contents/Resources.
func inAppBundle(exe string) bool {
	return strings.Contains(exe, ".app/Contents/MacOS/")
}
