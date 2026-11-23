//go:build !darwin && !windows

package main

import "errors"

func runGUI(options) error {
	return errors.New("the desktop client runs on macOS and Windows; use -headless here")
}
