//go:build windows

package vdd

import (
	"fmt"
	"log"
	"time"

	"golang.org/x/sys/windows"
)

const pingFailuresBroken = 3

// track records v and starts pinging with the first display. d.mu is held.
func (d *Driver) track(v *VirtualDisplay) {
	d.displays[v.ID] = v
	if d.stopPing == nil {
		d.stopPing, d.pingDone = make(chan struct{}), make(chan struct{})
		go pingLoop(d.h, d.pingEvery, d.errc, d.stopPing, d.pingDone)
	}
}

// untrack forgets id and stops pinging after the last display. d.mu is held.
func (d *Driver) untrack(id GUID) {
	delete(d.displays, id)
	if len(d.displays) == 0 && d.stopPing != nil {
		close(d.stopPing)
		<-d.pingDone
		d.stopPing, d.pingDone = nil, nil
	}
}

func pingLoop(h windows.Handle, every time.Duration, errc chan<- error, stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	t := time.NewTicker(every)
	defer t.Stop()
	failures := 0
	for {
		select {
		case <-stop:
			return
		case <-t.C:
		}
		err := ping(h)
		if err == nil {
			failures = 0
			continue
		}
		failures++
		log.Printf("vdd: watchdog ping failed (%d in a row): %v", failures, err)
		if failures == pingFailuresBroken {
			select {
			case errc <- fmt.Errorf("vdd: driver stopped answering pings: %w", err):
			default:
			}
		}
	}
}
