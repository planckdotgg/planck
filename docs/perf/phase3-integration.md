# Phase 3 engine integration

`perf/phase3-engine` merges the four next-tier workstreams onto `main`
(`b5b5cda`) in contract order: defaults, VDD, Metal direct, PyroWave. They merged
with no conflicts. It then wires them through transport, session and both
commands. `internal/ui` is untouched; its settings screen and drawer come in
the next pass.

## What a session does now

1. Both Hellos carry `FeatureCapabilities`. The host sends its encoders
   (probed once), displays and SudoVDA state. The client sends its display,
   decoders, link (`pkg/linkinfo`), discovery route and overrides.
2. The host runs `defaults.Resolve` with its policy over the client's
   overrides. `SessionReady` carries the legacy projection, and `wire.Profile`
   carries the full profile. Older peers on either side get the old
   negotiation.
3. The profile sets the codec, chroma, depth, size, fps, start and ceiling
   bitrate, slices, NVENC split and FEC floors.
4. With a virtual display, the session makes a SudoVDA monitor on the encode
   GPU, captures it, and removes it when the session ends.
5. PyroWave frames go out as chunk-aligned slices with critical-slice
   parity. A client hands an incomplete frame on with its lost chunks zeroed
   once the next frame starts, and never asks for a keyframe.

## Live results (2026-11-23, bigboi → M5 Pro over wired 2.5 GbE, ports 41200–41202)

Both links reported wired at 2500 Mbps, with 1452-byte datagrams.

| Run | Profile | Frames | E2E p50 / p95 ms |
|---|---|---|---|
| Auto, `-fps 120` | VDD 5120x1440@120 on the RTX 4080, HEVC Main10, split | 810 in 15 s, 0 lost | 4.72 / 5.30 |
| `-codec pyrowave -res 1920x1080` | VDD 1920x1080@120, PyroWave 4:4:4, 470 Mbps | 782 in 15 s, 0 lost, 0 partial | 6.70 / 8.35 |
| Synthetic 1080p120, `-codec pyrowave` | PyroWave 4:4:4 on the 4080 | 1308 at 120.1 fps, 0 lost | 3.79 / 6.79 |
| Synthetic 1080p120, `-codec hevc` | HEVC Main10, 4 sub-frame slices | 1393 at 120.7 fps, 0 lost | 3.77 / 4.36 |

- The virtual-display runs deliver about 60 fps because Desktop Duplication
  sends only changed frames, and the test's moving window updates at the
  WinForms timer rate.
- PyroWave E2E does not include its Metal decode yet: the decoder reports
  no latency. On real capture, its one-way network time was 5.4 ms p50,
  which is about 350 datagrams per frame sent from Go on Windows.
- Teardown on disconnect removed the monitor about 1 s after the client left.
  After a hard kill of the host, SudoVDA's watchdog removed it 2.30 s after
  the kill. Polling with `planck-vdd info` keeps an orphan alive, because
  every IOCTL except GET_WATCHDOG resets the watchdog; check the screen list
  instead.

## Open items

- `internal/ui`: settings for every override, Decisions and Warnings in the
  drawer, and the drawer state for direct mode. Until then, the client
  mirrors the HUD hotkey and treats a click in the pill band as opening the
  drawer.
- 10-bit PyroWave output on Metal and `Drawable10Bit`: clients report
  `Present10Bit` false.
- Partial PyroWave frames wait for the next frame's first shard; there is
  no 1 ms timer.
- `transport.Reassembler.PartialFrame` is unused; the session receives
  through `fec.Decoder`.
- The PyroWave pixel-rate seeds come from WS4's idle 1080p timings, and the
  startup encode probe that would replace them does not exist yet.
