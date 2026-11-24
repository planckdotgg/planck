package tailscale

import (
	"context"
	"errors"
	"testing"
)

// TestStatusFallsBackToCLI: with no LocalAPI endpoint the status comes
// from the tailscale command; a failing command keeps the LocalAPI error.
func TestStatusFallsBackToCLI(t *testing.T) {
	noAPI := func() (endpoint, error) { return endpoint{}, ErrNotRunning }
	c := &Client{resolve: noAPI, dial: dialEndpoint,
		cli: func(context.Context) ([]byte, error) { return []byte(fakeStatus), nil }}
	st, err := c.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Self.ID != "nSELF" || len(st.Peers) != 2 || !st.Peers[0].Online || st.Peers[0].TailscaleIPs[0].String() != "100.104.23.37" {
		t.Fatalf("status from the CLI: %+v", st)
	}

	c.cli = func(context.Context) ([]byte, error) { return nil, errors.New("exit status 1") }
	if _, err := c.Status(context.Background()); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("err = %v, want ErrNotRunning", err)
	}
}
