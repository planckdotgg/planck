//go:build !darwin && !windows && !linux

package main

import "errors"

func runGUI(options) error {
	return errors.New("the desktop client runs on macOS and Windows; use -headless here")
}
