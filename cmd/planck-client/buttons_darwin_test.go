package main

import (
	"os"
	"testing"

	"github.com/planckdotgg/planck/pkg/input"
)

func TestOtherButtonNumbers(t *testing.T) {
	for n, want := range map[int64]input.Button{2: input.ButtonMiddle, 3: input.ButtonX1, 4: input.ButtonX2, 5: 0, 0: 0} {
		if b, ok := otherButton(n); b != want || ok != (want != 0) {
			t.Fatalf("button number %d: %d %v, want %d", n, b, ok, want)
		}
	}
}

// TestMacButtonsReadsTheSystem: the system's buttons read without fail;
// with PLANCK_LIVE_INPUT_TEST=1, and no button held, none is down. A fresh
// reader takes the middle until a button past the right comes.
func TestMacButtonsReadsTheSystem(t *testing.T) {
	m := newButtonSys(nil, nil)
	idle := os.Getenv("PLANCK_LIVE_INPUT_TEST") == "1"
	for b := input.ButtonLeft; b <= input.ButtonX2; b++ {
		if m.down(b) && idle {
			t.Fatalf("button %d down", b)
		}
	}
	if b, ok := m.other(); b != input.ButtonMiddle || !ok {
		t.Fatalf("other before any event: %d %v", b, ok)
	}
}
