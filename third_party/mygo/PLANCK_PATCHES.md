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

Phase 1B added, in the same files:

| File | What |
|---|---|
| `gpusurface/gpusurface_windows.go` | `Resize(w, h)`: unbinds and frees the back buffer view, `ResizeBuffers` (keeping the tearing flag; 0 takes the window's client size), views the new back buffer. `PresentSlice(texture, slice, width, height)`: presents one slice of a texture array, as Media Foundation decoders write their pictures (`D3D11_SRV_DIMENSION_TEXTURE2DARRAY`), showing its top-left width x height through a per-frame UV scale in the vertex shader (decoder textures are aligned past the picture, as 1920x1088). The wrap cache is keyed by texture, slice and size. `Present(t)` is `PresentSlice(t, 0, 0, 0)`. |
| `gpusurface/gpusurface_darwin.go` | `Resize(w, h)`: the layer's frame (in points) and drawable size (in pixels). |
| `gpusurface/gpusurface_other.go` | `Resize` returns an error. |

Phase 1B polish added:

| File | What |
|---|---|
| `gpusurface/gpusurface.go` | `Options.Composite` (Windows; macOS ignores it). |
| `gpusurface/gpusurface_windows.go` | With `Composite`: `CreateSwapChainForComposition` at the window's client size (flip-discard, `DXGI_ALPHA_MODE_IGNORE`, the tearing flag kept when the factory takes it, else dropped), on a `DCompositionCreateDevice` device of the surface's own, as the root visual of `CreateTargetForHwnd(hwnd, FALSE)`: a target that is not topmost, under the topmost one a MyGo `Transparent` window draws its UI through, the pattern of `internal/windows/webview_embed.go`. `Resize(0, 0)` takes the client size itself (a composition swap chain has no window), and `Close` detaches the visual. Pass the `MyGoSurface` child window, where MyGo's target is, not the top-level `NativeHandle()`: a target on the top-level window does not show under the child. |
| `gpusurface/gpusurface_windows_test.go` | `TestComposite`: a composite surface takes the client size, follows a resize, and its frames reach the screen (`GetPixel` on the desktop DC). |

### API

```go
s, err := gpusurface.New(gpusurface.Options{Window: w.NativeHandle(), Timings: n})
s.Present(frame) // IOSurfaceRef on macOS, ID3D11Texture2D* on Windows
s.PresentSlice(tex, slice, w, h) // Windows: one slice of a decoder's texture array, cropped
s.Resize(w, h)   // after the window's size changed
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
   - On Windows the surface needs an HWND with no swap chain, or `Composite`
     to sit under a MyGo native-UI window's own target. Its caller finds the
     `MyGoSurface` child by class name; upstream could expose the content
     HWND (or a `platform.SurfaceNative`) so callers need not.
   - The zero-allocation call path (`syscall6`) may belong in MyGo's
     `internal/darwin` for every hot path, not just this package.
   - `Resize` is explicit: the app calls it from its resize event. MyGo's
     window could call it itself once the surface sits in its view tree.
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
