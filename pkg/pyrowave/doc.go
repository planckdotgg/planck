// Package pyrowave is a Go port of PyroWave, Hans-Kristian Arntzen's
// intra-only wavelet video codec for local game streaming
// (https://github.com/Themaister/pyrowave, MIT, bitstream v1 at a32b05e0).
// The shader math in shaders/ is ported from the upstream MIT shaders; see
// LICENSE.pyrowave for the upstream notice.
//
// Every frame stands alone: a 5-level CDF 9/7 wavelet per component, 32x32
// coefficient blocks coded as raw bit-planes, and exact single-pass rate
// control. The host encodes with Direct3D 11 compute shaders on the capture
// GPU; clients decode with Metal (macOS) or Direct3D 11 (Windows) compute
// shaders straight into a presentable texture. Package ref holds a CPU
// encoder and decoder used as the reference in tests.
//
// Transport framing (chunk.go): a frame is a run of chunks of the session's
// shard size, each holding whole 32-bit records. A missing chunk decodes as
// zero coefficients for its blocks, so loss blurs a region instead of
// freezing the stream. The codec is meant for LAN links only.
package pyrowave
