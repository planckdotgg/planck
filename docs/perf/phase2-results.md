# Planck Phase 2 latency work: what changed and what it measured

Branch `perf/latency-opt`, on top of the Phase 1 audit (`perf/latency-audit`, `docs/perf/phase1-latency-audit.md`). Measured 2026-11-23 on the Phase 1 hardware and paths: host `bigboi` (RTX 4080, RX 6700 XT driving a 60 Hz monitor), client the M5 Pro over wired 2.5GbE (`192.168.188.126` to `192.168.188.165`), ports 41100/41101, the Phase 1 config dirs. The Mac's load average was 6 to 15 throughout (as in Phase 1), so base and new ran interleaved, one after the other per scenario, 15 s each, first 240 frames skipped in traces.

## Changes

| plan item | change |
|---|---|
| P1a | `pkg/player` decodes on the session's decode goroutine and presents from its own thread through a mailbox of one: a blocked `nextDrawable` never holds decode, a superseded picture goes back to the decoder at once. `gpusurface` (macOS) reports each frame's `presentedTime` (`OnPresented`, `PresentTagged`); `ClientStats.Glass` and `ClientStats.E2EGlass` carry it to the HUD, and client traces get `presentat` and `glassat` columns. |
| P3 | `encode.Config.Split` (auto, on, off), replacing `PLANCK_NVENC_SPLIT`. Auto: 1440 lines and up, or 1080 lines from 200 fps, one codec slice, LAN paths only. `planck-host -split`. |
| P4, P11 | `encode.SliceEncoder`: NVENC hands each slice on as its lock reports it, and the host sends it at once (`SendVideoSlice`). Locks are 25 us apart with the core yielded between them. |
| P5 | Client receive: a blocking `recvfrom` on its own thread at user-interactive QoS. `Hello.Features` negotiates 1452-byte datagrams when both ends route the LAN path over a 1500-byte MTU; Tailscale, WAN and older peers keep 1152. |
| P6 | VideoToolbox real-time session; one reused timer; the decode loop on a user-interactive thread. |
| P7 | Host receive: a blocking `recvfrom` on an MMCSS "Games" thread, input handled inline. Hosts drop duplicate input Seqs; for 2 s after a frame missed a datagram the client sends the previous event again ahead of each new one. |
| P8 | FEC: the first loss on a clean path sets the estimate to its report at once; every keyframe block carries at least one parity shard. |
| P10, P2b | Encoder rate and VBV sized for the display's refresh when it is below the fps asked. `IDXGIDevice::SetGPUThreadPriority(7)` on the encoding device (it took effect unelevated on both GPUs). `PLANCK_AMF_POLL` removed. |

## Results (base `df947ab` against this branch)

NVENC synthetic, LAN. E2E is host compositor present to the client's decoded picture (headless; no present). Milliseconds, p50/p95/p99.

| scenario | E2E base | E2E new | host encode base | host encode new | Mbps base/new |
|---|---|---|---|---|---|
| 1080p120, 1 slice | 3.90/4.58/5.00 | 3.49/3.94/5.64 | 1.44/1.75/1.83 | 1.44/1.75/1.82 | 32.5/32.7 |
| 1440p120, 1 slice (split) | 4.50/5.10/5.37 | 4.02/5.35/8.89 | 2.20/2.56/2.65 | 1.51/1.67/1.76 | 31.1/42.4 |
| 1080p240, 1 slice (split) | 2.96/3.35/3.96 | 2.72/3.29/3.54 | 1.36/1.55/1.61 | 1.04/1.11/1.16 | 32.7/45.0 |
| 1080p120, 4 slices sub-frame | 3.68/4.29/4.65 | 3.08/3.56/3.81 | 1.43/1.73/1.79 | 1.47/1.80/1.90 | 32.5/32.6 |

Sub-frame: the host's send after encode end fell from 142 us p50 to 1 us; the slices are already out.

Input one-way, client stamp to host handler (zero mouse moves every 20 ms), microseconds p50/p95/p99/max:

| scenario | base | new |
|---|---|---|
| 1080p120 | 172/1979/17561/73563 | 174/357/815/1789 |
| 1440p120 | 164/4373/12123/43361 | 143/272/644/2204 |
| 1080p240 | 118/2911/14413/36827 | 129/264/589/1380 |
| 1080p120 sub-frame | 177/2150/16984/66643 | 149/277/351/5665 |
| real capture, AMF, 60 Hz | 278/2333/21559/94933 | 227/330/406/1971 |

Present to glass in a composited 120 Hz window (`pkg/player` `TestPresentThread`): p50 8 to 10 ms, as Phase 1 measured. The GUI client stream was not measured live; `PLANCK_TRACE_CLIENT` from the GUI now records `glassat` for that.

## Findings on the way

- A goroutine locked to its thread and parked on Go's netpoller stalled 10 to 15 ms at a time on the loaded Mac (net one-way p99 11 to 13 ms, the rate controller fell to 10 Mbps at 1080p240). Unlocked it was fine; a blocking `recvfrom` on the locked thread is better than both. Locked goroutines woken by channels (decode loop, present thread) showed no stall.
- Setting the FEC estimate to every report above it read 2% steady loss as 10%; only the first loss on a clean path jumps now.
- NVENC's `sliceOffsets[0]` on a keyframe is past the parameter sets; the first streamed slice starts at 0.
- On Windows `recvfrom` returns a 32-bit `SOCKET_ERROR`; Windows reports the loopback MTU as -1.
- The sub-frame poll still keeps its thread on the CPU for the encode (about 1.4 ms a frame): Windows has no sleep finer than about 0.5 ms. The change cut the driver locks from about 480 to 53 a frame and yields the core between them.
- Split encode makes frames about a third larger at p50 and up to 75% at p95 (1440p120 46 against 35 KB p50; 42 against 31 Mbps sent).
- AMF tails on the RX 6700 XT are unchanged by GPU priority 7 (encode p99 8 to 12 ms); Phase 1's P2a (virtual display on the 4080) is still the fix.

## P1b, not done

`displaySyncEnabled` is already off; frames still go through WindowServer composition because MyGo's view, which draws the HUD and receives the stream's input, sits over the video layer. Direct-to-display needs: input taken from the video view or an event monitor instead of MyGo's view; the HUD drawn in the video's Metal pass; MyGo's view hidden while full screen with the HUD hidden. Expected gain about one refresh (8 ms at 120 Hz), to verify with the `glassat` column.

## Reproduce

Scripts and raw data: `C:\Users\cawat\tmp\oak-perf-bin2` (`host-ab.sh`, `abf.txt`, `abr.txt`, host and input CSVs) on bigboi and `/tmp/oak-perf-bin2` (client CSVs, JSON summaries) on the Mac. Summaries: `planck-trace -host host-<label>.csv` and `planck-trace -client client-<label>.csv`.
