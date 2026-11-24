package rtprio

import "testing"

func TestBoostOnExitingGoroutine(t *testing.T) {
	errc := make(chan error, 1)
	go func() { errc <- Boost() }()
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
}
