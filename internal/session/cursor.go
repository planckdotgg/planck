package session

import (
	"errors"
	"time"

	"github.com/planckdotgg/planck/pkg/capture/cursor"
	"github.com/planckdotgg/planck/pkg/wire"
)

// cursorPoll is how often the host looks at its cursor: a game taking or
// letting go of the mouse reaches the client within a frame or two.
const cursorPoll = 16 * time.Millisecond

// cursorLoop tells the client how the host's cursor stands, at the start
// and each time it shows, hides, or an app captures or frees it. It ends at
// once where the platform cannot tell.
func (st *stream) cursorLoop() {
	read := st.h.cfg.ReadCursor
	if read == nil {
		read = cursor.Read
	}
	t := time.NewTicker(cursorPoll)
	defer t.Stop()
	var last wire.CursorState
	sent := false
	for {
		s, err := read()
		if errors.Is(err, cursor.ErrUnsupported) {
			return
		}
		if err == nil {
			st.mu.Lock()
			d := st.info.Display
			st.mu.Unlock()
			m := cursorMessage(s, cursor.Rect{X: d.X, Y: d.Y, W: d.W, H: d.H})
			if !sent || m.Visible != last.Visible || m.Captured != last.Captured {
				if st.sess.SendControl(&m) == nil {
					last, sent = m, true
				}
			}
		}
		select {
		case <-st.ctx.Done():
			return
		case <-t.C:
		}
	}
}

// cursorMessage is s for the client, its place on the streamed display
// when it is on it.
func cursorMessage(s cursor.State, display cursor.Rect) wire.CursorState {
	m := wire.CursorState{Visible: s.Visible, Captured: s.Captured}
	m.X, m.Y, m.HasPos = cursor.Normalize(s.X, s.Y, display)
	return m
}
