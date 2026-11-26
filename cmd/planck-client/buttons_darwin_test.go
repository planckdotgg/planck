package main

import (
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

// TestMacButtonsReadsTheSystem: with no button held, as under test, the
// system holds none down; a fresh reader takes the middle until a button
// past the right comes.
func TestMacButtonsReadsTheSystem(t *testing.T) {
	m := newButtonSys(nil, nil)
	for b := input.ButtonLeft; b <= input.ButtonX2; b++ {
		if m.down(b) {
			t.Fatalf("button %d down", b)
		}
	}
	if b, ok := m.other(); b != input.ButtonMiddle || !ok {
		t.Fatalf("other before any event: %d %v", b, ok)
	}
}
