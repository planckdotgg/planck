// Package hostbuf keeps decoded Linux pictures in memory: NV12 or P010
// planes in memfd-backed mappings, which a Wayland compositor maps as
// wl_shm buffers with no copy, described by capture.DMABuf with Memory set.
package hostbuf
