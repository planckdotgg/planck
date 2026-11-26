// Package vaapi encodes HEVC on Linux with VA-API (Intel's iHD and AMD's
// radeonsi drivers), through libva.so.2 and libva-drm.so.2 loaded with
// purego (no cgo).
//
// Frames come as capture.DMABuf surfaces: a DMA-BUF is imported as a VA
// surface with no copy, planes in memory are uploaded. RGB is converted
// to 4:2:0 by the driver's video processor.
//
// Where the driver takes packed sequence and slice headers, the encoder
// writes the VPS, SPS, PPS and slice headers itself (package
// codec/hevc), as VA-API's parameters carry no conformance window;
// elsewhere the driver writes them all.
package vaapi
