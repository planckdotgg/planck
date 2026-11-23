# Planck patches to MyGo

Vendored from `github.com/egoist/mygo` **v0.10.0** (commit
`ebde9399baf2734b3361856a53f4036859973d1d`, MIT). Planck's `go.mod` replaces
the module with this directory.

## Left out of the vendored copy

Not part of the Go library, and not needed to build it: `website`, `docs`,
`docs-zh`, `examples`, `benchmarks` (Rust and egui comparisons), `packages`,
`plugins`, `scripts`, `native/internal/roundtrip`, `.github`, `.claude`, and
the Bun files (`bun.lock`, `package.json`, `tsconfig.json`, `.bun-version`,
`mise.toml`). Only tests import `plugins` (`internal/e2e`,
`internal/nativegen`), so those tests do not build here. Nothing else changed.

## Additions

Every Planck change is a new file. No upstream file is modified.

| File | What |
|---|---|
| `gpusurface/gpusurface.go` | Public API: `Options`, `Format` (BGRA8, NV12, P010), `Timing`, `Surface.Timings`. |
| `gpusurface/gpusurface_darwin.go` | Metal: a `CAMetalLayer` of the surface's own (2 drawables, `displaySyncEnabled = NO`, `framebufferOnly`), IOSurface planes wrapped with `newTextureWithDescriptor:iosurface:plane:` and cached by surface, one draw converting BT.709 video-range YUV, a presented handler reading `presentedTime` into `Timings`. |
| `gpusurface/ccall_darwin.go` | `syscall.syscall6` by `go:linkname`, as `golang.org/x/sys/unix` does: calls C with no Go heap allocation, where `purego.SyscallN` allocates once per call. |
| `gpusurface/gpusurface_windows.go` | D3D11: a flip-discard swap chain of the surface's own (3 buffers, `DXGI_SWAP_CHAIN_FLAG_ALLOW_TEARING`, `Present(0, DXGI_PRESENT_ALLOW_TEARING)`, `SetMaximumFrameLatency(1)`), plane SRVs of NV12/P010 textures (R8/R8G8, R16/R16G16), and `fastCall`, a fixed-arity COM call for the hot path. `Timings` has no `Presented` on Windows (see below). |
| `gpusurface/adapter_windows.go` | `Options.Adapter`: makes the device on a named DXGI adapter. |
| `gpusurface/gpusurface_other.go` | Other platforms: `New` returns an error. |

### API

```go
s, err := gpusurface.New(gpusurface.Options{Window: w.NativeHandle(), Timings: n})
s.Present(frame) // IOSurfaceRef on macOS, ID3D11Texture2D* on Windows
s.Close()
s.Timings()      // per frame: Submitted, Encoded, Presented (ns, gpusurface.Now's clock)
```

macOS also has `Device()` and `Queue()`; Windows `Device()`, `Context()`,
`SwapChain()`, `Tearing()` and `AdapterName()`. `Present` allocates nothing once each
frame of the decoder's pool was wrapped; the presented-time callbacks (`Timings`)
allocate, on Core Animation's thread.

## Upstream PR plan

1. `gpusurface` as is, behind its own package so apps that do not import it
   pay nothing (MyGo's "apps pay only for what they use" rule).
2. Open questions for upstream:
   - On Windows the surface needs an HWND with no swap chain. To sit inside a
     MyGo native-UI window it needs a child HWND or a DirectComposition
     visual under the window's own.
   - The zero-allocation call path (`syscall6`) may belong in MyGo's
     `internal/darwin` for every hot path, not just this package.
   - Window resizing is not handled: the drawable or buffers keep the size
     they had at `New`.
   - Windows present times: `GetFrameStatistics` read after each present on
     an RX 6700 XT and an RTX 4080 (Windows 11) gave `SyncQPCTime` values
     before the frame was submitted, so `Timings` leaves `Presented` at 0
     there. PresentMon (ETW, needs elevation) is the way to measure it.

## Findings worth reporting upstream

- `internal/gpu/d3d11`'s `call` is variadic and `go:uintptrescapes`: every
  COM call allocates on the Go heap (14 a frame in this surface before
  `fastCall`). The same holds for the renderer's draws.
- `purego.SyscallN` allocates once per call with arguments (v0.11.1, measured
  with `testing.AllocsPerRun`; none with no arguments). MyGo's Metal
  renderer sends its per-batch messages through it (`msg2`, `msg5`).
