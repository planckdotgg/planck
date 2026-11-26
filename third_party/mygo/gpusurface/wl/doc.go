// Package wl speaks the Wayland protocol on a display connection someone
// else opened, as GTK's, through libwayland-client loaded at run time with
// purego (no cgo).
//
// A Queue is a private event queue on that connection, with a registry of
// its own: objects made from it get their events there, dispatched on the
// goroutine that runs the queue, while the connection's owner keeps
// reading and dispatching its own. Interfaces the library does not export
// (viewporter, linux-dmabuf, presentation-time, pointer constraints,
// relative pointer, keyboard shortcuts inhibit) are defined here as
// libwayland's wl_interface tables, in Go memory.
//
// The package builds on Linux (amd64, arm64) only.
package wl
