package session

import (
	"bufio"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/planckdotgg/planck/pkg/capture/cursor"
	"github.com/planckdotgg/planck/pkg/wire"
)

// menuPoll is one cursor poll of testdata/oblivion-menu-polls.txt: the
// count of absolute moves injected so far, the last one's target, and the
// host's cursor in desktop pixels.
type menuPoll struct {
	moves            uint32
	tx, ty           uint16
	x, y             int
	visible, clipped bool
}

// oblivionMenuPolls are the cursor polls of a host trace, Oblivion
// Remastered's main menu on a 3840x2160 mode the game switched to from
// 5120x1440: the client's pointer free, the game's cursor hidden and never
// warped.
func oblivionMenuPolls(t *testing.T) []menuPoll {
	f, err := os.Open("testdata/oblivion-menu-polls.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var polls []menuPoll
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fs := strings.Fields(sc.Text())
		n := make([]int, 5)
		for i := range n {
			if n[i], err = strconv.Atoi(fs[i]); err != nil {
				t.Fatal(err)
			}
		}
		polls = append(polls, menuPoll{moves: uint32(n[0]), tx: uint16(n[1]), ty: uint16(n[2]), x: n[3], y: n[4],
			visible: fs[5][0] == 'V', clipped: fs[5][1] == 'C'})
	}
	return polls
}

// replayWarp runs polls through a warpCapture with the cursor placed on
// display, and returns each poll's verdict.
func replayWarp(polls []menuPoll, display cursor.Rect) []bool {
	var w warpCapture
	got := make([]bool, len(polls))
	for i, p := range polls {
		m := cursorMessage(cursor.State{Visible: p.visible, Captured: p.clipped, X: p.x, Y: p.y}, display)
		got[i] = w.observe(m, p.moves, p.tx, p.ty)
	}
	return got
}

// TestWarpCaptureOblivionMenu: the traced menu's hidden cursor, normalized
// over the 5120x1440 display the host still thought it captured, sat a
// fixed fraction off each move and left the display below y 1440; it took
// the client relative and back 14 times in 18 seconds. Neither on that
// stale display nor on the real one is it a capture.
func TestWarpCaptureOblivionMenu(t *testing.T) {
	polls := oblivionMenuPolls(t)
	for name, d := range map[string]cursor.Rect{
		"stale 5120x1440":  {W: 5120, H: 1440},
		"actual 3840x2160": {W: 3840, H: 2160},
	} {
		got := replayWarp(polls, d)
		if n := slices.Index(got, true); n >= 0 {
			t.Errorf("%s: captured at poll %d of %d (%d polls captured)", name, n, len(polls), count(got))
		}
	}
}

func count(b []bool) int {
	n := 0
	for _, v := range b {
		if v {
			n++
		}
	}
	return n
}

// TestWarpCaptureSkewedFollow: a hidden cursor that follows each move a
// constant fraction short, as one normalized over the wrong display does,
// moves with the moves and never counts, however slowly it goes.
func TestWarpCaptureSkewedFollow(t *testing.T) {
	for _, step := range []int{20, 60, 200, 600} {
		var w warpCapture
		var moves uint32
		w.observe(wire.CursorState{HasPos: true, X: 30000, Y: 20000}, moves, 0, 0)
		for i := range 400 {
			tx, ty := 30000+i*step%30000, 20000+i*step/3%20000
			moves++
			m := wire.CursorState{HasPos: true, X: uint16(tx * 3 / 4), Y: uint16(ty * 3 / 2)}
			if w.observe(m, moves, uint16(tx), uint16(ty)) {
				t.Fatalf("step %d: captured at poll %d", step, i)
			}
		}
	}
}

// TestWarpCaptureNoFlap: a recentring game that holds the cursor while the
// moves go away from the centre is captured once and stays so; it never
// goes free and back while the game keeps warping.
func TestWarpCaptureNoFlap(t *testing.T) {
	var w warpCapture
	var moves uint32
	w.observe(wire.CursorState{HasPos: true, X: 32768, Y: 32768}, moves, 0, 0)
	var got []bool
	for i := range 300 {
		moves++
		dx := uint16(1500 + i%7*900)
		tx, ty := 32768+dx, 32768-dx/2
		if i%2 == 1 {
			tx, ty = 32768-dx, 32768+dx/2
		}
		at := uint16(32768 + i%3)
		got = append(got, w.observe(wire.CursorState{HasPos: true, X: at, Y: at}, moves, tx, ty))
	}
	first := slices.Index(got, true)
	if first < 0 || first > warpPolls {
		t.Fatalf("first captured at poll %d, want by %d", first, warpPolls)
	}
	if i := slices.Index(got[first:], false); i >= 0 {
		t.Fatalf("let go at poll %d while the game kept warping", first+i)
	}
}
