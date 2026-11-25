//go:build !darwin && !windows

package power

func KeepAwake(string) (func(), error) { return nil, ErrUnsupported }
func LockScreen() error                { return ErrUnsupported }
