# WS4: PyroWave in pure Go with GPU compute

`pkg/pyrowave` ports PyroWave (Themaister/pyrowave, MIT, bitstream v1 at
`a32b05e0`) to Go with no cgo. It has four parts:

- a float32 reference codec (`ref/`);
- a Direct3D 11 `cs_5_0` encoder and decoder, compiled at run time with `d3dcompiler_47.dll`;
- a Metal decoder, compiled at run time with `newLibraryWithSource`;
- chunk framing for the transport.

Frames are synthetic (`internal/synth`), full of 1-pixel text, the hardest
case for an intra wavelet codec. All numbers were measured on 2026-11-23.

## Conformance and quality

- Upstream's MIT Metal encoder (built outside the repo) made three golden
  bitstreams. `ref` decodes them to upstream's own decode PSNR:
  32.679 against 32.678 dB, 30.360 against 30.360, and 33.869 against 33.868.
  Its decode of an upstream 720p stream is 64 dB from upstream's decode.
- Encoder against upstream at the same budget (1280x720, YUV PSNR):

| Mode | Upstream | Planck |
|---|---|---|
| 4:2:0, 1.6 bpp | 25.59 dB | 25.98 dB |
| 4:4:4, 3 bpp | 28.74 dB | 29.25 dB |
| 4:2:0, 4 bpp | 37.46 dB | 38.75 dB |

- The GPU encoders give exactly the reference encoder's PSNR on every
  adapter. GPU decodes are 60 to 94 dB from the reference decode. The
  10-bit case compares at 8 bits.
- RGB PSNR at 1080p: 24.2 dB at 1.6 bpp and 37.2 dB at 4 bpp (4:2:0), and
  26.6 dB for 4:4:4 10-bit at 4 bpp. On this content, 35 dB needs about
  4 bpp in 4:2:0.

## Framing and loss (1280x720, 4:2:0, 4 bpp; clean 37.01 dB)

- Every chunk is `ChunkLen` bytes (the shard size) and starts with the
  8-byte sequence header. A zero word ends a chunk. Records that do not
  fit are split at 8x8 boundaries. Overhead is 3.05% at 1080p.
- Losing chunks at random gives 33.9 dB at 1%, 29.3 dB at 5% and 16.6 dB at
  20%.
- Losing every critical slice (levels 4 and 3) still decodes, at 12.3 dB.
  Losing every chunk returns `ErrNoHeader`.
- CPU work on an M5 Pro, 1440p at 1.6 bpp: `Pack` 189 µs and `Parse`
  179 µs, both with 0 allocations.

## Encode on bigboi (1080p, back to back, MMCSS, NVIDIA power guard)

Wall time is the full `Encode` call: kernels, readback, and packing. Kernel
time covers convert through pack, from GPU timestamps.

| GPU | Case | Wall p50 / p99 | Kernels | Readback |
|---|---|---|---|---|
| RTX 4080 | 4:2:0 1.6 bpp | 0.70 / 0.74 ms | 0.30 ms | 0.13 ms |
| RTX 4080 | 4:2:0 4 bpp | 1.20 / 4.10 ms | 0.28 ms | 0.31 ms |
| RTX 4080 | 4:4:4 10-bit 4 bpp | 1.33 / 2.03 ms | 0.42 ms | 0.31 ms |
| RX 6700 XT | 4:2:0 1.6 bpp | 0.79 / 1.26 ms | 0.47 ms | 0.00 ms |
| RX 6700 XT | 4:2:0 4 bpp | 1.38 / 1.71 ms | 1.15 ms | 0.04 ms |
| RX 6700 XT | 4:4:4 10-bit 4 bpp | 1.53 / 4.92 ms | 0.85 ms | 0.06 ms |
| UHD 770 | 4:2:0 1.6 bpp | 9.35 / 19.7 ms | 8.91 ms | 0.04 ms |
| UHD 770 | 4:4:4 10-bit 4 bpp | 17.4 / 24.3 ms | 16.6 ms | 0.08 ms |

The 4080 sits in a PCIe 3.0 x4 slot, which explains its readback and upload
times. The UHD 770 is too slow for 1080p120 with these kernels.

## Decode

D3D11 GPU time on bigboi, from the start of the upload to the colour pass:

| GPU | 1.6 bpp | 4 bpp | 4:4:4 10-bit |
|---|---|---|---|
| RTX 4080 | 0.38 ms (upload 0.14) | 0.57 ms (upload 0.33) | 0.67 ms |
| RX 6700 XT | 0.39 ms | 0.97 ms | 0.73 ms |
| UHD 770 | 5.7 ms | 7.1 ms | 10.1 ms |

Metal GPU time on an M5 Pro, per command buffer, over 240 frames:

| Case | p50 | p99 |
|---|---|---|
| 1080p 4:2:0 1.6 bpp | 0.51 ms | 1.20 ms |
| 1080p 4:2:0 4 bpp | 0.51 ms | 1.20 ms |
| 1080p 4:4:4 4 bpp | 0.92 ms | 1.62 ms |
| 1440p 4:2:0 1.6 bpp | 0.83 ms | 1.50 ms |
| 3024x1964 4:2:0 1.6 bpp | 1.36 ms | 2.23 ms |

`Decode` itself (CPU parse and submit) takes 115 to 308 µs and does not
wait. Bytes only go from the CPU to the GPU.

## Not done yet, and known gaps

- Not measured: Windows encode at 1440p, and encode under a 3D load on the
  same GPU.
- Speed: the kernels store FP32 and use groupshared scans in place of
  SIMD-group ops. Upstream's ~0.15 ms is still 2 to 3 times faster.
  Candidate fixes are FP16 storage, `simd_*` on Metal, and fewer barriers
  in the 40x40 lifting tile.
- Metal 10-bit output returns `ErrUnsupported` until gpusurface presents
  BGR10A2 (WS2). D3D11 writes R10G10B10A2.
- `Probe`, the shader blob cache and `Frame.Latency` (0 for now) are left
  for the integration branch. Both decoders have `Wait` for tests.
- The encoder budget leaves out FEC parity, which the session adds.

## Reproduce

```
go test ./pkg/pyrowave/... ./pkg/transport/ ./pkg/encode/ ./pkg/decode/
go test -run xxx -fuzz FuzzRecords -fuzztime 5m ./pkg/pyrowave
PLANCK_LIVE_PYRO_TEST=1 go test -run TestLiveMetal -v ./pkg/pyrowave            # macOS
PLANCK_LIVE_PYRO_TEST=1 [PLANCK_LIVE_ADAPTER=NVIDIA] go test -run TestLiveD3D11 -v ./pkg/pyrowave  # Windows
```
