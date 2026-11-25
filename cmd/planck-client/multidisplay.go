package main

import (
	"fmt"

	"github.com/planckdotgg/planck/pkg/defaults"
)

// hostStreamsAtOnce is how many of its displays a Planck host streams to
// one client at once: one per session, which SwitchDisplay moves.
const hostStreamsAtOnce = 1

// displayPlan is the stream windows for a host's displays: one a display,
// the streamed display's first. Hidden counts the displays left without
// a window, which a switch reaches.
type displayPlan struct {
	windows []string
	hidden  int
}

// planDisplayWindows plans the windows of a stream from a host with
// displays ds, streaming streamed ("" on a virtual display). Without
// every, or with one display, the stream has its one window. With every,
// the host's displays each get one, the main display next after the
// streamed one, up to maxWindows and to the displays the host streams at
// once.
func planDisplayWindows(ds []defaults.HostDisplay, streamed string, every bool, maxWindows, atOnce int) displayPlan {
	p := displayPlan{windows: []string{streamed}}
	if !every || len(ds) < 2 {
		p.hidden = max(len(ds)-1, 0)
		return p
	}
	n := min(len(ds), max(maxWindows, 1), max(atOnce, 1))
	order := make([]defaults.HostDisplay, 0, len(ds))
	for _, d := range ds {
		if d.Primary {
			order = append(order, d)
		}
	}
	for _, d := range ds {
		if !d.Primary {
			order = append(order, d)
		}
	}
	for _, d := range order {
		if len(p.windows) == n {
			break
		}
		if d.ID != streamed {
			p.windows = append(p.windows, d.ID)
		}
	}
	p.hidden = len(ds) - len(p.windows)
	if streamed == "" {
		// A virtual display is no host display: every one is left.
		p.hidden = len(ds) - (len(p.windows) - 1)
	}
	return p
}

// displayNotice is what a stream that asked for every host display says
// when it opens fewer windows than the host has displays, "" otherwise.
func displayNotice(host string, every bool, p displayPlan) string {
	switch {
	case !every || p.hidden == 0:
		return ""
	case len(p.windows) == 1:
		return host + " streams one display at a time"
	}
	return fmt.Sprintf("%d more displays on %s", p.hidden, host)
}
