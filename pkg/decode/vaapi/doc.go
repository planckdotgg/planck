// Package vaapi decodes HEVC on Linux with VA-API (Intel, AMD, and
// NVIDIA through nvidia-vaapi-driver), through libva.so.2 and
// libva-drm.so.2 loaded with purego (no cgo). VA-API takes parsed
// parameters, which package codec/hevc reads from the stream.
//
// A picture leaves the decoder as DMA-BUFs (vaExportSurfaceHandle, one
// composed layer), which a Wayland compositor shows as they are, or,
// where the presenting surface does not take them, copied into memory.
package vaapi
