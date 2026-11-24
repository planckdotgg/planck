//go:build !windows

package cursor

// macOS keeps other apps' cursor state to itself: CGCursorIsVisible
// answers for the calling process alone.
func read() (State, error) { return State{}, ErrUnsupported }

func readShape(State) (Shape, error) { return Shape{}, ErrUnsupported }
