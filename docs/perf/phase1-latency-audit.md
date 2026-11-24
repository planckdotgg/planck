# Planck Phase 1 latency audit (main @ b9aa4e3)

Measured 2026-11-23 on real hardware. Host `bigboi`: i9-13900K, RTX 4080 (no display, PCIe x4), RX 6700 XT (drives the only monitor, 1920x1080 @ 60 Hz HDMI), UHD 770, Windows 11. Client `Christophers-MacBook-Pro`: M5 Pro, main display 5120x1440 @ 120 Hz, second display 3440x1440 @ 75 Hz.
Paths: LAN `192.168.188.126` to the Mac's wired 2.5GbE `192.168.188.165`, and Tailscale direct `100.104.23.37` to `100.74.237.90` (direct over the same LAN at `192.168.188.126:41641`, so the Tailscale rows show WireGuard overhead, not WAN distance).

All runs used ports 41100/41101, discovery off, a separate host config dir (`C:\Users\cawat\tmp\oak-perf-cfg`) and client config dir (`/tmp/oak-perf-ccfg`). Chris's `planck-host.exe` (PID 34372, ports 41000-41002) and `planck-client` were not touched.

## Summary

1. **Mac present is the largest stage.** Windowed `CAMetalLayer` NV12 1080p at 120 fps: submit to on-screen p50 10.3 ms, p95 20.4, p99 54.9 (60 fps: p50 17.0). Everything before present (capture to decoded picture) is 2.7 to 4.6 ms p50 on NVENC. The `present()` call itself blocks on `nextDrawable` up to p95 4.8 ms and, because it runs on the decode goroutine, delays the next decode.
2. **RX 6700 XT AMF tails come from inside AMF, not from Planck.** Encode p50 3.3 to 3.9 ms; p99 4.3 to 23.5 ms across 18 runs without added load (only one run was clean; 30.7 ms with a light 3D load); max up to 219 ms. Every spike sits in the one blocking `QueryOutput` call (SubmitInput p99 under 0.26 ms, Go overhead under 0.18 ms). They happen with synthetic input too (so not DDA), in runs with no bitrate change at all (and 0 of 42 spikes near a change in the run that had three), and with Planck polling instead of AMF waiting (no change). A light 3D load on the same GPU makes them worse (p99 30.7 ms). The brief's 22 to 40 ms E2E p99 reproduces as E2E p99 17.9 to 28.5 ms on real capture.
3. **Sub-frame streaming does nothing today.** NVENC's first slice is ready at 0.58 to 1.03 ms (about half the frame), but `nvenc.pollSubFrame` waits for the whole picture and `stream.videoLoop` sends whole frames. The client only reassembles whole frames. 1, 4 and 8 slices give the same E2E within noise.
4. **NVENC two-engine split encode is unused and wins 0.3 to 0.7 ms.** Forced split (`SplitMode 2`): 1440p120 encode p50 2.17 to 1.49 ms, E2E p50 4.83 to 4.13 ms; 1080p240 encode 1.35 to 1.03 ms, E2E 3.24 to 2.63 ms. Cost: frames are 32 to 33% larger at the same content, a compression loss to weigh at CBR.
5. **The Mac receive path drains about 8 to 14 us per datagram.** A 79 KB frame takes 0.6 to 1.0 ms p50 from first to last datagram on 2.5GbE (wire time 0.25 ms; the host emits it in 0.27 ms). This grows linearly with bitrate.
6. **The Windows host wakes about 1 ms late on received datagrams.** Host-measured RTT is 1.2 to 2.0 ms p50 against 0.4 to 0.6 ms from the client. Input one-way on LAN is p50 0.74 to 1.16 ms, p95 3.7 to 4.4, p99 4.9 to 8.6.
7. **Already fine:** host hot path is allocation-free (0 GCs in 20 s at 1080p240); client GC is negligible (one GC per 2 s, STW at most 0.1 ms); FEC parity is already 0% on a clean LAN (0.06 to 0.15% measured); AEAD + UDP is 10.5 to 15 us per datagram with 0 allocations; `transport.Mono()` is 43 ns with 0 allocations; client queue wait p50 is 25 to 80 us.

Estimated glass-to-glass today on NVENC LAN 1080p120 (excluding game render and DWM): about 3.5 ms to decoded picture + about 10.3 ms present p50, so about 14 ms p50. Fixing present (item P1) is worth more than every other item combined.

## Method

- Branch `perf/latency-audit` adds opt-in per-frame traces. `PLANCK_TRACE_HOST`, `PLANCK_TRACE_CLIENT` and `PLANCK_TRACE_INPUT` name CSV files. Rows are kept in memory (no allocation until 131072 rows) and written when the stream ends. `cmd/planck-trace` summarizes them.
- Host stamps are mapped to the client clock with the ping/pong offset of the lowest-RTT sample of the last 16 (error at most about RTT/2, about 0.2 to 0.3 ms on LAN).
- **E2E** here is host compositor present (DXGI `LastPresentTime`; synth: frame release) to the client's `Submit` returning a decoded picture. The headless client does not present. Present to glass was measured on its own with `cmd/spike-mygo-surface` (CAMetalLayer `presentedTime`).
- Each scenario streams 15 s; the first 120 frames (60 for 60 fps AMF) are skipped. Cells are **p50/p95/p99/max in microseconds** unless noted.
- **Caveats.** The Mac had load average 11 to 13 throughout (a qemu VM at 130% CPU, fseventsd at 100%, WindowServer 35%), so client p95 and above are inflated and vary between runs; p50 is reliable. The synthetic pattern compresses well, so frames are 18 to 79 KB (about 24 to 32 Mbps); network and reassembly stages understate a 50 to 150 Mbps game stream. bigboi's monitor is 60 Hz, so real capture is at most 60 fps whatever `-fps` asks. A Go build on bigboi overlapped one of the `s1080p120-s4sf-ts` / `s1440p120-s1-ts` runs.

## 1. Baseline per-stage breakdown

Host stages: **encode** (Encode call), **1st slice** (sub-frame only), **send** (FEC + AEAD + all `sendto` for the frame). Client stages: **net first** (encode end to first datagram read), **reassembly** (first to last needed datagram), **queue** (reassembled to decode loop), **decode** (VideoToolbox callback latency), **E2E**. DDA acquire latency, which only applies to real capture, is in table 1c.

### 1a. RTX 4080 NVENC synthetic, LAN 2.5GbE (HEVC NV12, CBR 50 Mbps cap, app-limited)

| scenario | KB | encode | 1st slice | send | net first | reassembly | queue | decode | E2E |
|---|---:|---|---|---|---|---|---|---|---|
| 1080p120, 1 slice | 36 | 1459/1763/1861/1969 | - | 143/201/237/384 | 346/1370/2912/4499 | 283/432/972/6388 | 34/150/2001/9327 | 1180/2750/4753/8461 | 3495/7318/10676/16655 |
| 1080p120, 4 slices | 36 | 1512/1797/1886/2309 | - | 150/200/254/346 | 400/2012/3988/8695 | 148/409/613/1971 | 78/251/3759/9396 | 1367/3429/5622/7864 | 3891/8787/12191/20499 |
| 1080p120, 4 slices sub-frame | 36 | 1532/1832/1899/2211 | 796/948/1014/1284 | 193/264/301/480 | 345/1239/2575/7145 | 282/465/533/1154 | 29/106/191/2371 | 1110/2326/3691/6764 | 3477/6438/9212/15127 |
| 1080p120, 8 slices sub-frame | 36 | 1576/1876/1941/2093 | 685/838/889/998 | 198/256/295/459 | 405/2432/4942/7268 | 172/370/822/6142 | 57/157/1146/6689 | 1356/3534/5396/11993 | 3976/8683/10823/20043 |
| 1440p120, 1 slice | 34 | 2351/2695/2894/3108 | - | 186/369/446/483 | 354/491/1957/5278 | 285/477/574/1731 | 36/94/162/1590 | 1358/1605/2961/11484 | 4550/5318/7976/17266 |
| 1440p120, 4 slices sub-frame | 34 | 2188/2516/2605/2694 | 1027/1213/1253/1358 | 139/201/250/304 | 332/713/1906/5464 | 250/406/514/2204 | 38/96/140/1296 | 1310/1733/3066/6581 | 4281/5308/7757/13159 |
| 1440p120, 8 slices sub-frame | 33 | 2234/2539/2602/2688 | 827/1008/1068/1167 | 131/168/190/224 | 286/445/820/23366 | 223/332/385/707 | 61/93/112/4012 | 1337/1732/2016/11394 | 4342/4825/5603/29689 |
| 1080p240, 1 slice | 18 | 1381/1575/1631/2190 | - | 85/113/131/192 | 235/335/695/43096 | 158/286/315/1375 | 29/71/104/5356 | 756/1028/1415/49354 | 2686/3103/4641/55322 |
| 1080p240, 4 slices sub-frame | 18 | 1356/1549/1621/1713 | 674/780/827/911 | 82/120/137/199 | 226/385/1370/4406 | 179/293/328/1566 | 33/75/138/3514 | 818/1013/2028/3791 | 2731/3347/5216/10003 |
| 1080p240, 8 slices sub-frame | 18 | 1420/1604/1665/1766 | 582/682/723/856 | 93/124/136/218 | 232/370/470/6933 | 210/317/358/742 | 36/56/76/1880 | 821/1053/1145/1903 | 2830/3209/3455/9863 |

Frames lost: 0 in every run; parity 0.06 to 0.15%; 2 to 4 frames dropped per session, all at start-up.

### 1b. Same, Tailscale direct

| scenario | KB | encode | send | net first | reassembly | queue | decode | E2E |
|---|---:|---|---|---|---|---|---|---|
| 1080p120, 1 slice | 36 | 1458/1760/1845/1950 | 119/160/201/329 | 483/1100/2659/6555 | 409/913/1168/3890 | 26/47/71/1354 | 1006/1262/1356/1835 | 3604/4539/6139/10125 |
| 1080p120, 4 slices sub-frame | 35 | 1499/1784/1884/2103 | 143/209/231/411 | 647/1155/1927/3786 | 507/999/1265/3654 | 26/56/82/1099 | 1058/1304/1473/1924 | 3873/4857/5605/7238 |
| 1440p120, 1 slice | 34 | 2251/2579/2664/2790 | 129/166/186/3303 | 572/970/1939/6974 | 366/731/905/1912 | 25/42/70/1282 | 1158/1398/1495/1985 | 4501/5266/6366/11115 |
| 1080p240, 1 slice | 18 | 1380/1573/1661/2059 | 68/108/125/191 | 472/974/1967/4306 | 188/405/624/1963 | 25/35/52/732 | 702/831/945/1789 | 2885/3549/4475/7592 |
| 1080p240, 8 slices sub-frame | 18 | 1459/1638/1695/1882 | 88/121/175/275 | 527/1078/2331/6000 | 233/516/747/3762 | 25/54/84/1332 | 756/951/1047/2127 | 3119/3870/5188/9049 |

Tailscale direct costs about +0.13 to 0.3 ms on the first datagram and +0.1 to 0.2 ms of reassembly at these sizes (userspace WireGuard on both ends), about 0.1 to 0.4 ms of E2E p50. Client RTT p50 0.62 to 1.31 ms against 0.37 to 0.55 ms on LAN.

### 1c. RX 6700 XT, real desktop capture (DXGI DDA, AMF HEVC) and synthetic AMF

A 640x480 topmost color-cycling window kept DDA at about 58 fps. "DDA" is compositor present to `AcquireNextFrame` returning.

| scenario | KB | DDA | encode | send | net total | decode | E2E |
|---|---:|---|---|---|---|---|---|
| real 60 fps, 1 slice, LAN | 3 | 139/179/217/2696 | 3402/3874/4291/5315 | 80/191/260/1669 | 451/1167/1375/1935 | 859/1588/2026/3083 | 5042/6508/7355/7830 |
| real 120 fps ask (60 Hz display), LAN | 21 | 142/189/210/16140 | 3397/6401/12306/55930 | 134/170/221/1839 | 525/865/1828/4087 | 906/1370/2737/6353 | 5171/9397/17906/58308 |
| real 60 fps, 4 slices, LAN | 3 | 140/179/236/16238 | 3503/9602/20669/27778 | 89/209/230/6124 | 386/761/1008/1238 | 897/1294/1712/2218 | 5316/11889/22487/29792 |
| real 60 fps, 1 slice, Tailscale | 3 | 65/181/203/16281 | 3401/6850/18472/219113 | 59/143/174/249 | 650/1951/3340/7010 | 849/1842/2325/19951 | 5343/9328/28474/221552 |
| synth AMF 60 fps | 79 | - | 3880/5611/10878/27390 | 268/389/424/565 | 940/1495/1849/5540 | 1448/1899/2316/10758 | 6709/8555/13401/25820 |
| synth AMF 60 fps, repeat | 78 | - | 3466/8410/17428/60360 | 259/421/500/573 | 1112/2569/2841/4445 | 1315/1653/1752/2294 | 6187/11498/21152/64182 |
| synth AMF 120 fps | 37 | - | 3052/5277/8637/190628 | 150/236/291/927 | 605/1215/1521/2312 | 1014/1705/2287/3162 | 4949/7874/10877/193890 |
| synth AMF 240 fps | 18 | - | 2789/3131/7483/52363 | 76/110/140/834 | 368/502/688/1978 | 811/1189/1437/2889 | 4129/4837/8919/55895 |
| synth AMF 60 fps + light 3D load on the AMD | 79 | - | 3405/7636/30694/66003 | 276/2356/3413/4006 | 850/3057/4157/4742 | 1457/1926/2295/3124 | 5921/10785/43781/69137 |

DDA itself is fast (present to acquire p50 0.14 ms); its max of about 16 ms is one missed refresh. Accumulated frames were never above 1.

### 1d. AMF encode decomposition and experiments (encode us)

| run | encode | SubmitInput | QueryOutput wait | outside AMF | spikes > 6 ms in QueryOutput |
|---|---|---|---|---|---|
| synth 60 fps | 4347/8080/11725/16061 | 154/225/255/315 | 4176/7896/11576/15880 | 24/34/41/52 | 154 of 154 |
| real 60 fps | 3788/8348/10665/32589 | 127/191/231/391 | 3626/8206/10538/32484 | 13/23/56/177 | 126 of 126 |

| experiment | encode p50/p95/p99/max | result |
|---|---|---|
| bitrate changes traced (rate120, rate60) | 3409/7200/23528/104741 and 3398/6368/14081/19128 | rate120: no changes, 54 spikes; rate60: 3 changes, SetBitrate 27 to 45 us, 0 of 42 spikes within 3 frames of a change |
| fixed 4 Mbps, no changes (60 and 120 fps) | 3335/6403/11700/31274 and 3360/7005/16822/35073 | spikes unchanged |
| AMF waits (`HevcQueryTimeout 50`) vs Planck spins (`PLANCK_AMF_POLL=1`), interleaved A/B/A/B | wait 3591/5024/9425/37621, 3465/4370/8394/15062; poll 3449/4485/8245/52606, 3340/4318/10634/34182 | no meaningful difference |

### 1e. NVENC forced two-engine split (`PLANCK_NVENC_SPLIT=2`), LAN

| scenario | KB | encode | E2E |
|---|---:|---|---|
| 1440p120 auto | 34 | 2171/2516/2647/3079 | 4829/5990/6748/10695 |
| 1440p120 split | 45 | 1490/1669/1758/2004 | 4125/5329/5847/6814 |
| 1440p120 split + 4 slices sub-frame | 34 | 2181/2514/2618/2660 (split ignored) | 4441/4998/5391/11357 |
| 1080p240 auto | 18 | 1349/1541/1601/1702 | 3243/4039/4563/6251 |
| 1080p240 split | 24 | 1030/1105/1181/1913 | 2627/3159/3652/5925 |

### 1f. Mac present to glass (`cmd/spike-mygo-surface`, NV12 1920x1080 drawable, window on the 120 Hz display), ms

| submit rate | present() call | submit to on-screen |
|---|---|---|
| 120 fps | 0.048/4.760/10.734/56.060 | 10.296/20.406/54.936/79.677 |
| 60 fps | 0.040/0.262/12.692/86.397 | 17.008/18.172/67.294/158.760 |

12 Go allocations per frame here come from the timing callbacks the benchmark enables; the player path without timings allocates nothing (`TestPresentAllocatesNothing`).

### 1g. Input one-way, client stamp to host handler (zero mouse moves every 20 ms, us)

| path | one-way | host handler | host-measured RTT |
|---|---|---|---|
| LAN, run 1 | 1158/3681/4927/6772 | 1/5/798/3614 | 2044/2917/3242/3584 |
| LAN, run 2 | 740/4443/8597/21085 | 1/2/2/13 | 1244/2386/2721/2721 |
| Tailscale | 508/6757/64307/204446 | 1/2/2/151 | 1270/2000/2422/2422 |

In the Tailscale run, 30 of 59 late inputs (> 3 ms) overlapped an AMF encode spike, against 6% expected by chance; in LAN run 2, 15 of 72 against 7.4%. In LAN run 1 there were no encode spikes and still 74 late inputs.

### 1h. Hot-path micro-costs (`go test -bench -benchmem -cpu 4`; Mac M5 Pro / bigboi i9-13900K)

| what | Mac | bigboi | allocs |
|---|---|---|---|
| seal + UDP send + recv + open, one 1152-byte datagram, loopback | 15.0 us | 10.5 us | 0 |
| FEC slice path, 78 KB frame, 0% parity | 3.7 us | 3.3 us | 0 |
| FEC 4 slices, 20% parity, no loss | 11.1 us | 23.8 us | 8 (3.7 KB) |
| FEC 4 slices, 20% parity, 6% loss | 17.1 us | 34.8 us | 32 (13.7 KB) |
| FEC whole frame, 20% parity, 6% loss | 34.4 us | 81.2 us | 8 |
| live session, 78 KB frame, 0% loss | dial timed out under load | 0.74 ms/frame, 72 datagrams | 0.00014 per datagram |
| `transport.Mono()` (QPC) | - | 42.7 ns | 0 |

GC on a live 1080p240 session (20 s, 4000 input events): host 2 GCs, both during session setup, none while streaming; client 10 GCs, 4 MB heap goal, stop-the-world 0.03 to 0.1 ms.

Windows wait accuracy (overshoot past the deadline, us p50/p95/p99/max, and the waiting thread's CPU):

| target | time.Sleep | SleepUntilMono (spins last 2 ms) | hi-res waitable timer | hi-res timer + 100 us spin |
|---|---|---|---|---|
| 100 us | 415/567/1505/2255, 0% | 3/6/11/14, 38% | 411/435/492/955, 0% | 3/7/27/331 |
| 500 us | 517/758/1007/112813, 0% | 3/8/9/10, 51% | 484/739/927/982, 0% | 11/75/552/976 |
| 1 ms | 502/584/1093/3866, 0% | 4/189/901/3731, 12% | 40/568/1187/3300, 1% | 19/515/1413/10826 |
| 4.17 ms | 376/1851/7126/11761, 0% | 3/30/1438/6846, 17% | 393/1057/6694/17632, 0% | 341/1146/4099/10692 |

## 2. Root causes

**Mac present (largest).** With the layer composited by WindowServer, a frame shows at the compositor's next refresh after it is drawn, so latency is about one refresh plus the compositor's own frame: 10.3 ms at 120 Hz, 17 ms when the display drops to 60 Hz for 60 fps content. `displaySyncEnabled=false` only tears when the layer goes direct to the display, which needs a full-screen opaque layer with nothing composited over it. With `maximumDrawableCount=2`, `nextDrawable` blocks when both drawables are in flight (p95 4.8 ms at 120 fps). `player.Submit` decodes and presents in one call on the session's decode goroutine (`internal/session/client.go` decodeLoop, `cmd/planck-client/gui.go` surfaceSink), so a blocked present delays the next decode. That is the path by which the 3-frame queue (`frameQueue = 3`) fills with stale frames (queue tails of 2 to 9 ms in table 1a).

**RX 6700 XT AMF tails.** The delay is inside AMF, the driver or the VCN. Evidence: all spikes are in the single blocking `QueryOutput` call; synthetic input spikes too (not DDA); bitrate changes are not near spikes; Planck polling instead of AMF waiting changes nothing; the CPU side is under 0.4 ms. Two aggravating factors were measured. First, sharing the GPU with 3D work (p99 10.9 to 30.7 ms with a light 3D load); this GPU also composites the desktop and anything Chris runs on it. Second, a low submission rate (p95 3.1 ms at 240 fps against 5.0 to 8.4 ms at 60 fps), which fits power-state ramp-down between frames. AMD has no NVAPI-style per-app power policy in `pkg/gpu/power_windows.go` ("AMD, Intel and Apple encoders do not need it"), and this data contradicts that comment. Separately, at least one 200 ms event in the Tailscale run hit both the encoder and the input receive path, which points to a machine-wide stall (DPC or driver) as well. AMF is also 2.4x slower than NVENC at p50 (3.4 against 1.4 ms) and takes BGRA, so AMF does the color conversion itself. Which queue that conversion runs on was not verified.

**Sub-frame and slices.** NVENC sub-frame readback works (first slice at 45 to 55% of the encode), but `pkg/encode/nvenc/nvenc_windows.go` `pollSubFrame` spins until `numSlices >= want` and returns the whole picture, without yielding (it burns a core). `internal/session/stream.go` then calls `SendVideoFrame`; `Session.SendVideoSlice` exists and is never called. On the client, `internal/session/client.go` registers only `Handlers.VideoFrame`, and VideoToolbox decodes whole access units. Net effect: slices only add encode and send cost (about +0.05 to 0.1 ms), and E2E differences between 1, 4 and 8 slices are within run-to-run noise. Codec slices without sub-frame (`-slices 4`) also lower compression slightly.

**Client receive path.** The host emits a frame in 0.08 to 0.27 ms, but the Mac takes 8 to 14 us per datagram to read, decrypt and push it into FEC. Reassembly therefore scales with datagram count: 0.16 ms for 18 KB, 0.28 ms for 36 KB, 0.6 to 1.0 ms p50 for 79 KB. At a realistic 100 to 150 Mbps 1440p120 stream (100 to 160 KB frames, 90 to 140 datagrams) this becomes 1 to 2 ms. `wire.MaxDatagram` is 1152 bytes for Tailscale's MTU, even on a 1500-byte LAN. The receive goroutine and decode loop run at default macOS QoS, so under load they can land on efficiency cores. VT decode is synchronous (`VTDecompressionSessionDecodeFrame` flags 0) with a purego callback, a channel hop and a `time.After` allocation per frame, and no `kVTDecompressionPropertyKey_RealTime`.

**Windows host receive wake-up.** Media datagrams are read by a plain goroutine through Go's IOCP netpoller. The host-side RTT (which includes host receive wake-up) is 0.7 to 1.5 ms above the client-side RTT (which subtracts it). Input shares that path with feedback and pings, and gets no priority. MMCSS is applied only to the video thread.

**Pacer.** The session does not pace video: frames go out as one burst, which is fine on 2.5GbE at these sizes. `transport.Pacer` and `SleepUntilMono` are used only by the synthetic source and spikes. On Windows, Go's `time.Sleep` already uses the high-resolution waitable timer, so a sleep overshoots about 0.4 to 0.5 ms. Only spinning reaches microseconds, at 12 to 51% of a core, and a loaded machine still shows millisecond tails.

**FEC.** `fec.Policy` has no floor by default, so a clean LAN sends almost no parity (measured 0.06 to 0.15%). On the first loss the estimate rises with weight about 4 x shards / 2000 per report, so the frame that hit the first loss is already lost and recovery is a PLI or reference invalidation.

**Not a problem.** Allocations and GC on the host; AEAD cost; DDA acquire latency (0.14 ms p50); NVENC ultra-low-latency setup (P1, ULL tuning, CBR, one-frame VBV, infinite GOP, no B-frames, async event wait, 0 allocs/frame); the client queue in the normal case (p50 25 to 80 us, nothing queued ahead at p99).

## 3. Phase 2 plan, prioritized

Savings are estimates from the tables above unless marked measured.

| # | change | files | expected saving | risk / tradeoff | kind |
|---|---|---|---|---|---|
| P1a | Split decode from present on the client: decode every access unit on the decode goroutine (references need it); present only the newest decoded picture from a separate present thread (mailbox of 1); never block decode on `nextDrawable`. Plumb gpusurface `Timings` `presentedTime` into `ClientStats` so the GUI reports glass time. | `pkg/player/player.go`, `internal/session/client.go`, `internal/session/sink.go`, `cmd/planck-client/gui.go`, `third_party/mygo/gpusurface/gpusurface_darwin.go` | removes present head-of-line blocking (p95 up to 4.8 ms) and stale-frame queueing (queue p99 2 to 9 ms) | picture lifetime: a decoded frame must stay referenced until shown; the decoder pool must hold mailbox + shown + in-flight | pure Go |
| P1b | Full-screen direct-to-display on macOS: when the stream fills the screen, use an opaque full-screen layer with no composited overlay and draw the HUD into the same Metal pass, so `displaySyncEnabled=false` applies and WindowServer composition is skipped | `third_party/mygo/gpusurface/gpusurface_darwin.go`, `cmd/planck-client/surface_darwin.go`, `internal/ui/stream_hud.go` | about one refresh: roughly 8 ms p50 at 120 Hz (from 1f; to verify with `presentedTime`) | any overlay or non-opaque pixel drops direct mode; tearing; the HUD must move into the video pass | pure Go (upstreamable to egoist/mygo) |
| P2a | Capture through a virtual display rendered on the RTX 4080. SudoVDA is installed; `HKLM\SOFTWARE\SudoMaker\SudoVDA\gpuName` is currently "AMD Radeon RX 6700 XT", and Apollo's config already uses `adapter_name = NVIDIA GeForce RTX 4080` with NVENC. With the render adapter set to the 4080, DDA on the virtual output gives textures on the 4080 and NVENC encodes them with no copy. It also allows 120/240 Hz capture (the physical monitor is 60 Hz). Planck would create and remove the display through SudoVDA's device interface (`DeviceIoControl`, pure Go). | new `pkg/capture/vdd` (Windows), `internal/session/platform_windows.go` (`openDisplay` adapter choice), host flags | encode p99 from 8 to 23 ms (AMF) to 1.9 ms (NVENC, measured); E2E p99 from 17.9 to 28.5 ms to about 5 to 6 ms | Chris: admin to set `gpuName` (needs a driver restart); games must render on the 4080 (Windows GPU preference), otherwise DWM copies across adapters; the IOCTL interface must be read from SudoVDA's sources | driver config for Chris + pure Go |
| P2b | AMF experiments, measured with the new trace columns: NV12 input converted by Planck with the D3D11 video processor (removes AMF's own conversion); `IDXGIDevice::SetGPUThreadPriority` and `D3DKMTSetProcessSchedulingPriorityClass` (Sunshine raises both; per Sunshine's log messages the high values need admin, not verified here); a steady encode cadence when capture is idle | `pkg/encode/amf/amf_windows.go`, `pkg/capture/dxgi/device_windows.go`, `internal/win` | unknown; the target is the p99 of 8 to 23 ms | the cause is inside the driver, so these may not help; priority may need an elevated helper | pure Go, then elevated follow-up |
| P2c | Chris, elevated: capture a GPUView/ETW trace and run LatencyMon during a real-capture AMF stream to tell VCN completion from DPC or ISR stalls (amdkmdag, Tailscale wintun) | none | diagnosis | needs admin | hardware/driver follow-up |
| P3 | Turn on NVENC split encode where slices are not in use: `SplitMode 2` for 1440p and up and for 1080p at 240 fps | `pkg/encode/nvenc/register_windows.go`, `pkg/encode/encode.go` (a Config field or policy) | measured: 1440p120 encode -0.68 ms, E2E -0.7 ms; 1080p240 encode -0.32 ms, E2E -0.6 ms | 32 to 33% larger frames at the same quality, so lower quality at a fixed CBR; check visually; NVENC ignores split when sub-frame slices are on | pure Go |
| P4 | True sub-frame streaming: an encoder API that hands each slice out as NVENC reports it (`lockNumSlices` and `sliceOffsets` per poll, yielding between polls); `stream.videoLoop` calls `SendVideoSlice` per slice | `pkg/encode/encode.go`, `pkg/encode/nvenc/nvenc_windows.go`, `internal/session/stream.go`, `internal/session/media.go` | host-side overlap of transmission with encode: 0.1 to 0.3 ms on 2.5GbE at measured sizes; on slow links about (N-1)/N of frame serialization (for example a 156 KB frame at 300 Mbps Wi-Fi: about 3 ms) | VideoToolbox still decodes whole frames, so the client gains only the transfer overlap; slices cost compression; split encode is lost (P3), so pick per path: split on LAN, slices on slow links | pure Go |
| P5 | Client receive path: a dedicated receive thread at `QOS_CLASS_USER_INTERACTIVE` (`pthread_set_qos_class_self_np` through purego); decrypt and FEC on the same thread without channel hops; on LAN paths use about 1452-byte datagrams (22% fewer packets) | `pkg/transport/session.go`, `pkg/wire/media.go` (path-dependent `MaxDatagram`), `pkg/transport/client.go`, new `internal/apple` QoS call | reassembly -0.2 to 0.8 ms at 80 to 160 KB frames; smaller p99 under load | `ShardSize` is fixed at the handshake, so it must be negotiated per path; Tailscale stays at 1152 | pure Go |
| P6 | VideoToolbox: set `kVTDecompressionPropertyKey_RealTime`; reuse one timer instead of `time.After` per frame; run the decode loop at user-interactive QoS | `pkg/decode/vt/vt_darwin.go`, `internal/session/client.go` | about 0.1 to 0.3 ms p50 (not measured); smaller tails | low | pure Go |
| P7 | Windows host receive: read the media socket on a dedicated OS thread with MMCSS, handling input before anything else; send redundant input copies on loss | `pkg/transport/session.go`, `internal/win`, `internal/session/host.go` | input one-way p50 -0.5 to 1 ms, p95 -3 ms (from 1g) | a blocking read thread needs a clean shutdown path | pure Go |
| P8 | FEC first-loss response: on a loss report, set the estimate to at least the reported sample at once; always send one parity shard on keyframes | `pkg/fec/policy.go`, `pkg/transport/fec.go` | avoids a keyframe round trip (tens of ms) on the first loss of a burst | about 1% more bytes on keyframes | pure Go |
| P9 | Pacing only where it helps (Wi-Fi, DERP): pace video at 1.5 to 2x target in 0.5 ms groups with at most 100 to 200 us of spin; keep LAN bursts | `pkg/transport/session.go`, `pkg/transport/pacer.go` | fewer burst losses on Wi-Fi; no gain on LAN | adds up to one group interval of delay | pure Go |
| P10 | Encode at the display's real rate: when the capture rate is below the requested fps, configure AMF/NVENC rate and VBV for the measured rate | `internal/session/media.go`, `internal/session/platform_windows.go` | correct VBV sizing; small | low | pure Go |
| P11 | Stop `pollSubFrame` from spinning a full core (yield or a short sleep between polls), whatever happens to P4 | `pkg/encode/nvenc/nvenc_windows.go` | CPU, not latency | adds up to the poll interval | pure Go |

Suggested order: P1a, then P3 and P6 (small and measured or low risk), then P1b, P5, P7, P4. P2a in parallel as Chris's setup step, because it removes the AMF problem on this machine outright.

## 4. Branch, tools and data

- Branch `perf/latency-audit` on `github.com/planckdotgg/planck`, pushed, no PR opened; this document is its last commit. Changes are opt-in and off by default:
  - `internal/session/trace.go`: per-frame host, client and input traces (`PLANCK_TRACE_HOST`, `PLANCK_TRACE_CLIENT`, `PLANCK_TRACE_INPUT`), with a column contract test (`trace_test.go`).
  - `internal/session/media.go`, `stream.go`, `client.go`, `host.go`: stage stamps, AMF split timings, bitrate-change timing, queue depth.
  - `pkg/transport/session.go`: `Session.FirstArrival` (first datagram time per frame).
  - `cmd/planck-trace`: summarizes the CSVs (p50/p95/p99/max per stage).
  - `cmd/planck-client`: `-input-probe-every`.
  - `pkg/transport/timer_windows_test.go`: Windows wait-accuracy report (`PLANCK_TIMER_REPORT=1`) and `BenchmarkMono`.
  - Audit-only experiments: `PLANCK_AMF_POLL=1` (`pkg/encode/amf`) and `PLANCK_NVENC_SPLIT` (`pkg/encode/nvenc/register_windows.go`). Phase 2 should replace these with real configuration rather than keep env knobs.
- Checks run on the final head: `go vet` and `go test` for `./internal/session ./pkg/transport ./cmd/planck-client` on macOS and for `./internal/session ./pkg/transport ./pkg/encode/amf ./pkg/encode/nvenc ./cmd/planck-host` on Windows, all `ok`. The new contract test fails when a traced field is renamed (checked).
- Raw data: CSVs, JSON summaries and the scripts that ran the matrix are in `/tmp/oak-perf-bin` (Mac) and `C:\Users\cawat\tmp\oak-perf-bin` (bigboi). The temporary worktrees were removed; the branch keeps everything reusable.

## Not verified

- Glass-to-glass with the GUI client presenting a live stream (present was measured with the surface benchmark, not under the stream).
- Real capture on the RTX 4080 through a virtual display (P2a); nothing was installed or reconfigured.
- Realistic bitrates of 50 to 150 Mbps: the synthetic pattern stays at 18 to 79 KB per frame.
- Client tails on an idle Mac; this one was loaded throughout.
- Quality cost of split encode and of slices (only frame sizes were compared).
- Pre-existing, outside this change: `gofmt -l` flags import order in `pkg/transport/{client,identity,server,session}.go` on main; `BenchmarkSessionVideo/loss0pct` timed out on dial on the loaded Mac.
