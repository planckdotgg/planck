# Phase 0, Spike 2: pure-Go capture, encode, and multi-GPU offload

Measured 2026-11-23 on `bigboi` (Windows 11, RTX 4080 + RX 6700 XT + UHD 770)
and `Christophers-MacBook-Pro` (macOS 27.0, Apple M5 Pro). Every binary was
built with `CGO_ENABLED=0`. Raw output is in [phase0-spike2/](phase0-spike2/).

## Verdicts

1. **Pure Go is enough on both platforms.** NVENC, AMF, oneVPL, DXGI Desktop
   Duplication, and Direct3D 11 run through `syscall` vtables on Windows.
   ScreenCaptureKit and VideoToolbox run through `purego` on macOS, including
   an Objective-C `SCStreamOutput` class called on a GCD queue, completion
   blocks, and VideoToolbox C callbacks from its own threads. No Swift or
   Objective-C shim is needed. A 60 s 1080p120 capture and encode soak ran
   7,200 frames with no crash, RSS flat at 69 MiB after warm-up.
2. **Multi-GPU offload loses on this machine.** Direct3D 11 cannot open a
   texture across adapters (`E_INVALIDARG` for NT and legacy handles on AMD
   and Intel). The staging round trip costs 2 to 15 ms per frame, more than
   a whole same-GPU encode. End to end on the real desktop, same-GPU AMF is
   3.6 ms p50 present to packet; capture on AMD and encode on the RTX 4080 is
   9.4 ms p50. The RTX 4080 sits in a PCIe x4 slot (Gen1 at idle, Gen3 max),
   which makes the copy slower here than on a typical x16 machine.
3. **GPU power state dominates NVENC latency.** Paced at 120 or 240 fps the
   RTX 4080 stays in P8 (210 MHz core, 405 MHz memory) and HEVC 1080p encode
   is 2.7 to 3.3 ms p50 with p99 up to 70 ms (127 ms at 1440p240). Back to
   back it is 1.3 to 1.4 ms p50, 1.6 to 4.0 ms p99. The host needs a power
   policy, such as the per-application "prefer maximum performance" profile
   Sunshine sets.
4. **Sub-frame readback works and tightens the tail.** With 4 or 8 HEVC
   slices, the first slice is readable 0.7 ms (1080p) or 1.0 to 1.2 ms
   (1440p) after submit, against 1.4 / 2.3 ms for the whole frame, and the
   polling path's p99 is 2.6 ms where the event path's is 8.4 ms.

## Adapter topology (`bigboi`)

| DXGI index | Adapter | LUID | Outputs |
|---|---|---|---|
| 0 | AMD Radeon RX 6700 XT | 0x11687 | `\\.\DISPLAY1` 1920x1080 @ 60 Hz, 10 bpc, SDR |
| 1 | NVIDIA GeForce RTX 4080 (driver 616.92, NVENC API 13.1) | 0x162e9 | none |
| 2 | Intel UHD Graphics 770 | 0x15cfd | none |
| 3 | AMD Radeon RX 6700 XT (second LUID) | 0x268a9 | none |
| 4 | NVIDIA GeForce RTX 4080 (second LUID) | 0x24f3c | none |
| 5 | Microsoft Basic Render Driver | 0x15ca0 | none |

The only active display is on the AMD card, so Desktop Duplication can only
run there, at 60 Hz. Capture at 120 or 240 Hz, and same-GPU capture on the
RTX 4080, need a display or virtual display (Parsec VDA or SudoMaker) bound
to that GPU at that rate. That changes Chris's display layout and was not
done. Encoder numbers at 120 and 240 fps use synthetic desktop-like frames
already on the GPU.

Vendor runtimes loaded with `syscall.LazyDLL`:

| Vendor | DLL | Result |
|---|---|---|
| NVIDIA | `nvEncodeAPI64.dll` | API 13.1; HEVC 8/10-bit, HEVC 4:4:4 8/10-bit, AV1 8/10-bit 4:2:0 (no AV1 4:4:4), 2 engines, sub-frame readback, intra refresh |
| AMD | `amfrt64.dll` | AMF 1.4.37; RX 6700 XT: H.264, HEVC Main10, 16 streams, slice output; no AV1 (RDNA2) |
| Intel | `libvpl.dll` | dispatcher finds `mfx-gen` API 2.8 on UHD 770 (`a780`); session opens; HEVC profiles 1/2/3/4/9 incl. 4:4:4 (AYUV/Y410), AVC, VP9, MPEG-2, JPEG |

`libmfx64-gen.dll` and `vpl.dll` are not in System32 here; the dispatcher
finds the GPU runtime in the driver store.

## NVENC on the RTX 4080

Encode latency is from `nvEncMapInputResource` to the bitstream copied out,
in ms, 1200 measured frames. "Paced" submits at the frame rate (the GPU stays
in P8); "back to back" submits as fast as frames finish (the GPU boosts).
Input is a D3D11 texture; NVENC converts BGRA and RGB10 to YUV itself.
0 Go allocations per frame in every NVENC case.

| Codec | Chroma | Input | Size | Paced p50 / p95 / p99 | Back to back p50 / p95 / p99 |
|---|---|---|---|---|---|
| HEVC | NV12 | BGRA | 1080p120 | 2.95 / 7.36 / 70.6 | 1.41 / 1.51 / 1.69 |
| HEVC | NV12 | NV12 | 1080p120 | 2.73 / 5.39 / 11.7 | 1.34 / 2.85 / 3.97 |
| HEVC | P010 | P010 | 1080p120 | 2.95 / 6.98 / 12.1 | 1.34 / 1.46 / 1.58 |
| HEVC | P010 | RGB10 | 1080p120 | 3.26 / 4.50 / 6.21 | 1.39 / 1.47 / 1.59 |
| HEVC | 4:4:4 | BGRA | 1080p120 | 4.26 / 6.24 / 15.5 | 2.26 / 4.98 / 8.59 |
| HEVC | 4:4:4 10-bit | RGB10 | 1080p120 | 4.24 / 5.86 / 7.88 | 2.07 / 2.16 / 2.44 |
| AV1 | NV12 | BGRA | 1080p120 | 3.42 / 5.14 / 6.97 | 1.91 / 8.39 / 12.0 |
| AV1 | P010 | P010 | 1080p120 | 2.98 / 4.22 / 8.99 | 1.34 / 1.39 / 1.58 |
| HEVC | NV12 | BGRA | 1080p240 | 2.94 / 5.28 / 8.35 | 1.41 / 5.45 / 7.98 |
| AV1 | NV12 | BGRA | 1080p240 | 2.56 / 9.17 / 17.6 | 1.56 / 1.93 / 4.30 |
| HEVC | NV12 | BGRA | 1440p120 | 4.94 / 8.12 / 18.5 | 2.25 / 4.38 / 8.69 |
| HEVC | P010 | P010 | 1440p120 | 4.33 / 6.17 / 9.46 | 2.19 / 2.26 / 2.88 |
| HEVC | 4:4:4 | BGRA | 1440p120 | 5.72 / 7.86 / 11.4 | 3.35 / 3.95 / 5.44 |
| AV1 | NV12 | BGRA | 1440p120 | 5.10 / 8.05 / 18.5 | 2.65 / 3.08 / 5.36 |
| AV1 | P010 | P010 | 1440p120 | 4.67 / 7.02 / 11.0 | 2.29 / 2.40 / 3.27 |
| HEVC | NV12 | BGRA | 1440p240 | 5.18 / 67.9 / 127 | 2.30 / 4.89 / 8.38 |
| AV1 | NV12 | BGRA | 1440p240 | 2.78 / 3.99 / 11.3 | 2.61 / 3.79 / 6.80 |

Slices, sub-frame readback, intra refresh, and split encode, back to back,
HEVC NV12 from BGRA:

| Size | Mode | Encode p50 / p95 / p99 | First slice p50 / p99 |
|---|---|---|---|
| 1080p240 | 1 slice, async event | 1.41 / 5.45 / 7.98 | |
| 1080p240 | 4 slices, async event | 1.45 / 2.01 / 3.95 | |
| 1080p240 | 4 slices, sub-frame poll | 1.44 / 1.64 / 1.91 | 0.78 / 1.13 |
| 1080p240 | 8 slices, sub-frame poll, split off | 1.51 / 1.76 / 1.96 | 0.70 / 1.14 |
| 1080p240 | intra refresh every 240 frames | 1.42 / 3.80 / 6.74 | |
| 1080p240 | split encode off | 1.40 / 1.58 / 3.24 | |
| 1440p240 | 4 slices, sub-frame poll | 2.27 / 2.44 / 2.63 | 1.19 / 1.54 |
| 1440p240 | 8 slices, sub-frame poll, split off | 2.33 / 2.55 / 3.72 | 1.01 / 1.85 |

Output bit rate held at or below the CBR target in every case (CBR, one-frame
VBV, P1 preset, ultra-low-latency tuning, infinite GOP).

## Desktop Duplication capture (AMD display, 1080p60)

600 frames with a small animated window driving motion. Times are from the
compositor's `LastPresentTime` (QPC) to our `clock.Now()` (QPC).

| Metric | p50 | p95 | p99 | max |
|---|---|---|---|---|
| Acquire interval | 16.68 | 17.10 | 33.16 | 33.76 |
| Present → acquire | 0.058 | 0.211 | 0.480 | 1.38 |
| GPU copy to own texture + sync | 0.353 | 0.557 | 1.20 | 3.27 |
| Present → frame ready on GPU | 0.412 | 0.796 | 1.43 | 4.41 |

No accumulated (missed) presents; 6 Go allocations per frame, from COM out
parameters that escape (fixable).

## Cross-adapter transfer

Per frame: copy on the source GPU, then the destination GPU holds the frame
in a default texture, with both GPUs synchronized. 300 frames per row, ms.

| From → to | Size | Format | Method | p50 | p99 | Breakdown p50 |
|---|---|---|---|---|---|---|
| RTX 4080, same GPU | 1080p | BGRA | CopyResource | 0.028 | 0.091 | |
| RTX 4080, same GPU | 1440p | BGRA | CopyResource | 0.115 | 1.73 | |
| RTX 4080 → any | any | any | Shared NT or legacy handle | fails: `E_INVALIDARG` on open | | |
| RTX 4080 → RX 6700 XT | 1080p | BGRA | staging | 3.94 | 4.19 | readback 2.57, CPU 0.53, upload 0.84 |
| RTX 4080 → RX 6700 XT | 1080p | NV12 | staging | 2.26 | 7.68 | readback 1.23, CPU 0.49, upload 0.41 |
| RTX 4080 → RX 6700 XT | 1440p | BGRA | staging | 9.71 | 33.4 | readback 4.74, CPU 2.35, upload 1.62 |
| RTX 4080 → RX 6700 XT | 1440p | NV12 | staging | 3.45 | 6.16 | readback 2.08, CPU 0.81, upload 0.53 |
| RTX 4080 → UHD 770 | 1080p | BGRA | staging | 3.48 | 4.19 | readback 2.57, CPU 0.54, upload 0.35 |
| RTX 4080 → UHD 770 | 1080p | NV12 | staging | 6.03 | 18.1 | readback 1.33, CPU 1.70, upload 2.31 |
| RTX 4080 → UHD 770 | 1440p | BGRA | staging | 10.85 | 24.3 | readback 4.72, CPU 3.59, upload 1.57 |
| RX 6700 XT → RTX 4080 | 1080p | BGRA | staging | 3.97 | 4.42 | readback 0.89, CPU 0.52, upload 2.54 |
| RX 6700 XT → RTX 4080 | 1080p | NV12 | staging | 1.73 | 2.11 | readback 0.40, CPU 0.21, upload 1.12 |
| RX 6700 XT → RTX 4080 | 1440p | BGRA | staging | 15.44 | 31.0 | readback 1.52, CPU 0.96, upload 12.9 |
| UHD 770 → RTX 4080 | 1080p | BGRA | staging | 4.32 | 4.68 | readback 1.54, CPU 0.23, upload 2.55 |

Uploads into the RTX 4080 are the slow leg: 14.7 MB in 13 ms is about
1.1 GB/s, consistent with its x4 link. Converting to NV12 on the source GPU
before the copy halves the cost.

## End to end on the real desktop (`bigboi`)

AMD `DISPLAY1` at 1080p60, HEVC 8-bit 30 Mbit/s, 600 frames, from the
compositor's present stamp to the encoded packet in Go memory, ms.

| Path | Transfer p50 / p99 | Encode p50 / p99 | Present → packet p50 / p95 / p99 / max |
|---|---|---|---|
| DDA (AMD) → AMF on the same GPU | none | 3.32 / 4.28 | 3.58 / 3.96 / 5.97 / 13.5 |
| DDA (AMD) → staging → NVENC (RTX 4080) | 1.55 / 2.38 | 7.59 / 14.1 | 9.37 / 14.8 / 17.1 / 19.9 |

The NVENC leg is slow here because a 60 fps load keeps the RTX 4080 in P8.

## AMF on the RX 6700 XT (paced, same matrix shape)

| Chroma | Input | Size | p50 / p95 / p99 |
|---|---|---|---|
| NV12 | BGRA | 1080p120 | 2.78 / 3.20 / 5.61 |
| P010 | P010 | 1080p120 | 2.64 / 4.86 / 11.4 |
| NV12 | BGRA | 1080p240 | 2.77 / 4.45 / 10.7 |
| NV12 | BGRA | 1440p120 | 4.52 / 6.42 / 9.31 |
| P010 | P010 | 1440p240 | 4.27 / 6.35 / 8.73 |

AMF held the CBR target exactly; 4 Go allocations per frame (vtable call
helper builds its argument slice).

## macOS: VideoToolbox on the M5 Pro

HEVC hardware encode and decode, synthetic IOSurface pixel buffers, paced,
600 measured frames. Encode is from `VTCompressionSessionEncodeFrame` to the
output callback's sample copied out; decode is from
`VTDecompressionSessionDecodeFrame` to the decoded image callback. Low-latency
rate control was accepted for HEVC. ms.

| Chroma | Input | Size | Encode p50 / p95 / p99 | Decode p50 / p95 / p99 |
|---|---|---|---|---|
| NV12 | BGRA | 1080p120 | 5.05 / 5.35 / 5.58 | 0.87 / 1.05 / 1.12 |
| NV12 | 420v | 1080p120 | 5.15 / 5.57 / 5.74 | 0.91 / 1.15 / 1.26 |
| P010 | x420 | 1080p120 | 5.27 / 5.52 / 5.71 | 0.93 / 1.14 / 1.27 |
| P010 | BGRA | 1080p120 | 5.45 / 5.71 / 5.86 | 0.79 / 0.91 / 0.95 |
| NV12 | 420v | 1080p240 | 5.24 / 6.00 / 6.23 | 0.98 / 1.41 / 1.54 |
| NV12 | BGRA | 1440p120 | 8.24 / 8.66 / 9.21 | 1.31 / 1.62 / 1.81 |
| NV12 | 420v | 1440p120 | 7.35 / 7.71 / 7.90 | 1.39 / 1.69 / 1.79 |
| P010 | x420 | 1440p120 | 7.08 / 7.68 / 7.85 | 1.15 / 1.63 / 1.79 |
| NV12 | 420v | 1440p240 | 7.39 / 8.03 / 8.27 | 1.42 / 1.95 / 2.06 |
| P010 | any | 1440p240 | every frame dropped by VideoToolbox in real-time mode | |
| 4:4:4 | any | any | refused: no HEVC 4:4:4 encoder on Apple silicon | |

At 1440p the synchronous encode (7 to 8 ms) is longer than a 240 Hz frame
(4.2 ms), so 1440p240 needs frames in flight, not one at a time.

## macOS: ScreenCaptureKit into VideoToolbox

Main display (5120x1440 @ 120 Hz) scaled by ScreenCaptureKit, a small
click-through window driving motion, 600 measured frames. "Display" is
`SCStreamFrameInfoDisplayTime`. Negative values mean the frame reached our
callback before its display timestamp. ms.

| Size | Format | Display → callback p50 / p95 / p99 | Interval p50 / p99 | VT encode p50 / p99 | Display → packet p50 / p95 / p99 |
|---|---|---|---|---|---|
| 1080p | 420v | -1.55 / 0.28 / 0.71 | 9.00 / 13.99 | 7.08 / 8.09 | 5.58 / 7.60 / 8.34 |
| 1440p | 420v | -1.63 / 0.26 / 0.52 | 9.00 / 14.28 | 8.19 / 8.79 | 6.59 / 8.57 / 9.03 |
| 1080p | BGRA | -1.67 / 0.24 / 1.24 | 9.01 / 14.80 | 6.90 / 8.02 | 5.30 / 7.47 / 8.64 |
| 1440p | BGRA | -1.62 / 0.33 / 3.25 | 9.01 / 15.66 | 8.96 / 10.09 | 8.01 / 10.47 / 14.68 |

Soak, 1080p120 420v capture and encode for about 66 s: 7,251 complete
frames, 36 idle, 21 dropped by our one-deep hand-off queue, display to packet
5.10 / 7.59 / 8.95 ms p50 / p95 / p99, no crash, RSS 63 MiB at 10 s and
69.2 to 69.4 MiB from 40 s to the end, 2 goroutines left.

## purego cost

| Call style | ns/op | allocs/op |
|---|---|---|
| `purego.RegisterFunc` (reflective) | 124 | 2 |
| `purego.SyscallN` | 49 | 0 |

The SCK plus VT path makes about 80 reflective calls per frame (165 allocs,
about 10 µs). Moving the per-frame calls to `SyscallN` would make it
allocation free. Not done in this spike.

## Interfaces

- `pkg/gpu`: adapter identity (LUID, vendor, name).
- `pkg/capture`: `Source.Next(timeout) (Frame, error)`, `Release`, `Close`.
  `Frame` carries the GPU surface handle, owning adapter, format (BGRA8,
  RGB10A2, RGBA16F, NV12, P010), size, present and acquire stamps, and dirty
  rects. `ErrTimeout` and `ErrAccessLost` are distinct.
- `pkg/encode`: `Config` (codec, chroma NV12/P010/YUV444/YUV444P10, source
  format, size, fps, bit rate, intra refresh period, slices, sub-frame,
  encode adapter), `Encoder.Encode(frame, forceKey, dst)`, `SetBitrate`,
  `Caps`. `ErrUnsupported` and `ErrDropped` are distinct. Encoders refuse
  what the hardware cannot do instead of degrading.

The spike surfaced one interface change: encoders need the source surface
format when the session opens (NVENC fixes input bit depth then), so it is in
`Config.Source`.

## Not done, and risks

- Capture above 60 Hz and same-GPU capture on the RTX 4080 on `bigboi`: no
  display is attached to that GPU (see topology).
- D3D12 cross-adapter heaps (`D3D12_HEAP_FLAG_SHARED_CROSS_ADAPTER`) would
  replace the CPU copy with a GPU copy over PCIe. Not built; it cannot beat
  the x4 link here and the same-GPU path already wins.
- An AMF or Quick Sync encode on frames that moved across adapters, and a
  real Quick Sync encode: the oneVPL work stops at caps and session.
- NVENC power policy (NVAPI DRS profile) is a finding, not implemented.
- An uncaught `NSException` from a wrong selector aborts the process and Go
  cannot recover it; backends should check selectors with
  `respondsToSelector:` when they open.
- Synthetic content: our NV12 test frames take the top-left chroma sample,
  which produces more chroma detail than a filtered conversion, so NV12-input
  rows show a higher output bit rate than BGRA-input rows.

## Reproduce

```sh
CGO_ENABLED=0 go build -o planck-spike2 ./cmd/planck-spike2
# Windows
planck-spike2 enum
planck-spike2 vendors
planck-spike2 nvenc -matrix -frames 1200            # paced
planck-spike2 nvenc -matrix -frames 1200 -pace=false
planck-spike2 dda -frames 600
planck-spike2 xfer -frames 300
planck-spike2 xfer -from AMD -to NVIDIA -formats bgra,nv12 -frames 300
planck-spike2 amf -frames 600
planck-spike2 pipeline -frames 600
# macOS
planck-spike2 vt -frames 600
planck-spike2 sck -frames 600
planck-spike2 sck -soak 60s
```

`dda`, `pipeline`, and `sck` show a small animated square in a screen corner
while they run, so the compositor has new frames. It never takes focus or
moves the cursor.
