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

v0.4 Phase 3A (HDR10 output) added:

| File | What |
|---|---|
| `gpusurface/gpusurface.go` | `Options.HDR10` (macOS; Windows ignores it). |
| `gpusurface/hdr_darwin.go` | New file. `SetHDR10(on)` and `HDR10()`: on, the `CAMetalLayer` gets `wantsExtendedDynamicRangeContent = YES` and `colorspace` = `CGColorSpaceCreateWithName(kCGColorSpaceITUR_2100_PQ)` (CoreGraphics, loaded with purego); off gives back the colour space the layer had when the surface was made (retained at `New`, NULL on a new layer) and standard range. It needs BGR10A2 drawables (`ErrHDRNeeds10Bit` otherwise). Call it from the presenting thread, between presents. |
| `gpusurface/gpusurface_darwin.go` | `Options.HDR10` makes BGR10A2 drawables and calls `SetHDR10(true)`. A BGR10A2 surface also compiles `fs_yuv2020` (BT.2020 non-constant-luminance YUV to R'G'B', left PQ encoded) and the overlay's `overlay_fs_pq`; `Present` draws YUV frames with the first while HDR10 is on. `Close` releases them and the kept colour space. A `New` that fails now closes the surface it was building (it called `Close` on the nil result before, a crash on any setup error). |
| `gpusurface/overlay_darwin.go` | `overlay_fs_pq`: the overlay's sRGB colour made linear, moved to BT.2020 primaries, set at 203 nits for white (ITU-R BT.2408 reference white) and PQ encoded, premultiplied after. The overlay's coverage moved into `overlay_color`, which both fragment functions call: Metal does not let one fragment function call another. |
| `gpusurface/hdr_darwin_test.go` | `TestHDR10Layer`: the layer's EDR and colour space on, off and on again, the HDR pipelines built, presents with an overlay, and an 8-bit surface refusing HDR10. |

SDR presentation is unchanged: the layer keeps no colour space, as before,
so SDR frames show as they did. HDR10 metadata (mastering display, MaxCLL)
is not set on the layer: the stream carries none yet.

Linux (Wave 1 client) added:

| File | What |
|---|---|
| `gpusurface/gpusurface.go` | `Options.Redraw`: in picture mode, called after each frame so the window draws it. |
| `gpusurface/gpusurface_other.go`, `gpusurface/direct_stub.go` | Build tags leave out `linux && !android && (amd64 \|\| arm64)`, which has its own files. |
| `gpusurface/gtk_linux.go` | GTK 3 / GDK through purego: `NativeOf(GtkWindow*)` finds the backend (Wayland `wl_display`/`wl_surface`/`wl_seat`, or X11 `Display*`/XID), the toplevel `GdkWindow`, the widget MyGo draws into (a `GtkGLArea` when MyGo runs on the GPU) with its place and scale, and `EventWindow`, the input-only `GdkWindow` MyGo sets its cursors on. |
| `gpusurface/gpusurface_linux.go` | `Surface` on Linux. `Present` takes a `*DMABuf`, a fixed-layout frame descriptor (fourcc, modifier, planes, DMA-BUF fds, offsets, pitches, optional CPU plane pointers, memfd flag, serial) that Planck's `capture.DMABuf` mirrors. Wayland gets a subsurface; anything else (X11, or a compositor without `wl_subcompositor`) gets picture mode. `Accepts(fourcc, modifier)`, `Wayland()`, `Layout()`, `Size()`, `Scale()`, `OnPresented`, `PresentTagged`. Direct mode, the overlay calls and `ScreenSize` are no-ops or `ErrUnsupported` here: the MyGo window draws the HUD itself. |
| `gpusurface/wayland_linux.go` | Desync `wl_subsurface`s placed below MyGo's surface: a black backdrop over the content area and the video fitted in it (`FitRect`), both scaled by `wp_viewporter`. Without `wl_subcompositor` or `wp_viewporter` the surface uses picture mode. Frames go as `zwp_linux_dmabuf_v1` buffers when the compositor lists their fourcc/modifier, else as `wl_shm` buffers copied (NV12/P010 to XRGB8888, BT.709) into a memfd pool. `wp_presentation` feedback gives `Timings.Presented`, converted from the compositor's clock to `Now`'s. Every object is on a private event queue (`wl.Queue`), so GTK keeps its own dispatch. |
| `gpusurface/picture_linux.go` | Picture mode: three NV12 buffers swapped as the overlay's rects are; `Bitmap()` and `Refresh()` show the newest frame through MyGo's `Bitmap.UpdateYUV`, under the UI. P010 shows its 8 high bits. It needs the frame's planes in memory (`DMABuf.CPU`), so it runs on X11, Xvfb, WSLg and software rendering. |
| `gpusurface/convert_linux.go` | BT.709 video-range YUV to XRGB8888 for the shm path. |
| `gpusurface/memfd_linux_{amd64,arm64}.go` | `memfd_create` syscall numbers (319, 279). |
| `gpusurface/wl/` | New package. libwayland-client through purego: proxies, `marshal_array_flags`, listeners as purego callbacks, private queues with their own registry, and the `wl_interface` tables for protocols the library does not export (viewporter, linux-dmabuf v1, presentation-time, subcompositor, pointer constraints v1, relative pointer v1, keyboard shortcuts inhibit v1), generated from the protocols' XML. |
| `gpusurface/gpusurface_linux_test.go` | BT.709 conversion, padded-pitch conversion, picture mode's P010 high bits, and `TestPresentLive` (gated on a display): 30 frames into a real window, Wayland or X11. |

The Linux DMA-BUF import path is written to the protocol but has not been tried on a
compositor yet: WSLg lists no dmabuf formats. Linux allocations per frame
have not been measured.

### API

```go
s, err := gpusurface.New(gpusurface.Options{Window: w.NativeHandle(), Timings: n})
s.Present(frame) // IOSurfaceRef on macOS, ID3D11Texture2D* on Windows
s.PresentSlice(tex, slice, w, h) // Windows: one slice of a decoder's texture array, cropped
s.Resize(w, h)   // after the window's size changed
s.Close()
s.Timings()      // per frame: Submitted, Encoded, Presented (ns, gpusurface.Now's clock)
s.SetHDR10(on)   // macOS: BT.2020 PQ in an EDR layer, or back to SDR
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
