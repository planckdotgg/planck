//go:build !windows

package session

import (
	"errors"

	"github.com/planckdotgg/planck/pkg/input"
)

// liveInputArea is Windows': elsewhere a pipe's input area holds until it
// reopens.
func liveInputArea(string) (input.Rect, input.Rect, error) {
	return input.Rect{}, input.Rect{}, errors.ErrUnsupported
}
