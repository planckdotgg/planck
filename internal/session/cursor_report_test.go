package session

import (
	"slices"
	"testing"

	"github.com/planckdotgg/planck/pkg/wire"
)

// TestCursorReportHoldsLoneHide: a game's cursor hidden for one poll, as
// it swaps its shape over a menu, never reaches the client; a hide that
// lasts goes a poll late, and showing, capturing and freeing go at once.
func TestCursorReportHoldsLoneHide(t *testing.T) {
	shown := wire.CursorState{Visible: true, Captured: true, HasPos: true}
	hidden := wire.CursorState{Captured: true, HasPos: true}
	free := wire.CursorState{Visible: true, HasPos: true}
	var r cursorReport
	var got []bool
	for _, m := range []wire.CursorState{
		shown, shown, hidden, shown, shown,
		hidden, hidden, hidden, shown,
		free, shown, {}, free,
	} {
		due := r.due(m)
		if due {
			r.sent(m)
		}
		got = append(got, due)
	}
	want := []bool{
		true, false, false, false, false,
		false, true, false, true,
		true, true, true, true,
	}
	if !slices.Equal(got, want) {
		t.Fatalf("due %v\nwant %v", got, want)
	}
}

// TestCursorReportRetriesHeldHide: a hide held a poll and then not sent
// goes on the next poll it still holds.
func TestCursorReportRetriesHeldHide(t *testing.T) {
	var r cursorReport
	r.sent(wire.CursorState{Visible: true})
	hidden := wire.CursorState{}
	if r.due(hidden) || !r.due(hidden) || !r.due(hidden) {
		t.Fatal("a lasting hide that failed to send did not come due again")
	}
}
