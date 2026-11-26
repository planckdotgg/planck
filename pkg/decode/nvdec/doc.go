// Package nvdec decodes HEVC and AV1 on NVIDIA GPUs under Linux with
// NVDEC, through libcuda.so.1 and libnvcuvid.so.1 loaded with purego (no
// cgo). NVDEC's own parser reads the bitstream; each picture is copied
// from the GPU into a memfd-backed buffer, which a Wayland compositor maps
// as wl_shm with no further copy and a picture-mode surface reads as it
// is.
package nvdec
