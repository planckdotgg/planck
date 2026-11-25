//go:build !windows

package input

// SendSAS is Windows' secure attention sequence; other systems have none.
func SendSAS() error { return ErrUnsupported }
