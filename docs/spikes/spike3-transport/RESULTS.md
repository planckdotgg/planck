# Phase 0 Spike 3: media transport

Question: for host to client game video, does `quic-go` RFC 9221 datagram delivery (with its congestion control) hold up at 50 to 150 Mbps and 120 to 240 fps, or does raw UDP sealed with keys exported from a QUIC control connection win? And what do sub-frame slicing and Reed-Solomon FEC buy under loss?

## Verdict

**Media goes over raw AEAD UDP. QUIC stays for control.**

1. **quic-go congestion control does throttle frame bursts, intermittently and badly.** On a clean wired LAN it kept up in 12 of 12 runs, but on the Mac's Wi-Fi downlink it collapsed in 2 of 12 (100 Mb@120 fps, 4 slices: send rate fell to 38 Mbps and frame p50 reached 8.3 s; 150 Mb@240 fps: p50 566 ms) with zero real packet loss. Over Tailscale at 150 Mbps it fell behind again (p50 20 ms, send slip p99 184 ms). The cause is loss-based congestion control reacting to Wi-Fi aggregation and reordering; `SendDatagram` blocks once its 32-frame queue is full, so the encoder stalls behind it. On loopback it never throttled, so this is a path effect, not CPU.
2. **Raw AEAD UDP wins on every axis we measured, decisively on robustness and allocations, modestly on median latency.** Unpaced raw never stalled on LAN or Wi-Fi; the one slow paced-raw run on Wi-Fi was host scheduling (frame p50 23.5 ms, but net p50 8.1 ms). Median network p50 across the 12 wired configs: raw 1.28 ms, quic-go 1.75 ms (quic-go worse in 12 of 12; on Wi-Fi 5.50 vs 7.21 ms). Allocations: raw 0.001 tx / 0.004 rx per packet; quic-go 4.0 tx / 12.5 rx per packet, which drove about 68 receiver GCs per 4.5 s run against 0.6 for raw.
3. **Planck needs its own rate control.** Dropping quic-go's congestion control for media means Planck must adapt bitrate itself from receiver feedback (delay gradient and loss, as in GCC or SCReAM). The ack channel built here already carries per-frame loss counts.
4. **Sub-frame slicing helps first-data latency, mostly at lower frame rates.** With 4 slices the first slice is ready at 1.09 ms vs 1.96 ms for the whole frame (raw, wired, 100 Mb@120 fps) and 1.43 vs 2.12 ms at 150 Mb@120 fps. At 240 fps frames are small and the gain is at most 0.2 ms (and negative at 50 Mbps, 0.61 vs 0.48 ms). The gain is only real if the decoder can start on a slice; that is a question for the encode/decode spike.
5. **FEC must be adaptive and sized per block.** At 1% loss without FEC only 64 to 70% of frames arrive whole; 20% parity restores 100%. At 5% loss, per-slice blocks need about 40% parity (the adaptive policy chose 39.6% and delivered 99.8 to 99.9%) while whole-frame blocks need about 20%, because small blocks have fatter binomial tails. The adaptive policy (smallest parity with P(loss > parity) < 1e-3 at the measured loss rate) used 2.6 to 10% parity on a clean link and tracked loss correctly.
6. **The largest tail-latency driver is not the transport: it is Windows frame scheduling on the host.** Send slip p99 (frame start minus its schedule) runs 0.2 to 105 ms on bigboi at 0 allocations; a pure busy-spin loop still shows p99 1.4 ms and max 13.6 ms. The real host loop must be driven by capture/encode events on a high-priority thread, not Go timers.

### Over Tailscale (WireGuard) vs direct LAN

Tailscale between these two nodes is direct (no DERP) over the same wired path, so the difference is WireGuard and the userspace data path. With the same 1100-byte shards (Tailscale MTU 1280):

| | direct LAN | Tailscale |
|---|---|---|
| min RTT | 0.18 to 0.25 ms | 0.33 to 0.40 ms (+0.15 ms) |
| raw burst net p50 (50 / 100 / 150 Mbps) | 0.69 / 0.99 / 1.30 ms | 0.97 / 3.15 / 1.53 ms |
| raw burst net p99 | 11.7 / 7.3 / 5.3 ms | 11.1 / 23.4 / 20.8 ms |
| frame jitter (RFC 3550, raw burst) | 0.22 to 1.33 ms | 0.19 to 2.84 ms |
| collapses at 150 Mbps | none | quic-go (p50 20 ms) and 500 Mbps paced raw (p50 636 ms, only 126 Mbps sent) |
| IPv4 bytes per media packet (1145-byte UDP payload) | 1173 | 1233 (+60 B: outer IPv4/UDP plus WireGuard header and tag, +5.1%) |

Every media packet is encrypted twice (Planck AES-128-GCM, then WireGuard ChaCha20-Poly1305). Tailscale is usable for 50 to 100 Mbps remote play with a few ms of extra tail, but direct UDP should be preferred whenever the peers can reach each other, and Tailscale should be treated as a fallback path that needs a lower bitrate ceiling.

LAN payload size (1360-byte vs 1100-byte shards) made no measurable difference on the wired LAN: raw burst, 4 slices, 240 fps p50 0.66 / 0.96 / 1.32 ms vs 0.69 / 0.99 / 1.30 ms.

## Setup

- Host (sender): bigboi, Windows 11, Intel I225-V 2.5GbE, `192.168.188.126`, Tailscale `100.104.23.37`.
- Client (receiver): MacBook Pro (arm64). It has two interfaces on the LAN: Thunderbolt 2.5GbE `en7` at `192.168.188.165` (primary route) and Wi-Fi 6 `en0` at `192.168.188.138` (5 GHz, 80 MHz, MCS 10, 1080 Mbps PHY, -35 dBm). Tailscale `100.74.237.90` (direct endpoint `192.168.188.165:41641`).
- Go 1.27.4, quic-go v0.65.0, klauspost/reedsolomon v1.14.2, `CGO_ENABLED=0`.
- Each run: a fresh QUIC connection, 200 pings to get min RTT, 0.5 s warm-up, then 4 s measured. Frames are constant-size (bitrate / fps) random bytes. Media shards: 1360 bytes on LAN (QUIC `InitialPacketSize` 1452), 1100 bytes on Tailscale (QUIC packet size 1200). Raw AEAD suite: AES-128-GCM.
- Loss and jitter are injected on the sender before the transport (independent random loss; jitter is a FIFO delay of 0 to 2 ms per packet).

### What the columns mean

- **p50 / p95 / p99**: frame delivery latency from the frame's scheduled time to the moment the receiver has every slice, estimated as ack arrival minus min RTT / 2. Includes host send slip.
- **net p50 / net p99**: the same, but from the moment the host actually started sending the frame. This isolates the transport and network from host scheduling.
- **1st-slice p50**: when the first slice was ready on the receiver (same clock basis as net).
- **jitter**: RFC 3550 interarrival jitter over per-frame delivery times.
- **send-slip p99**: how late the host started frames against their schedule.
- **allocs/frame**: `runtime.MemStats.Mallocs` delta over the measured window divided by frames, process-wide (includes quic-go internals).
- RTT/2 is optimistic when the two directions differ; the raw ack RTT (`ack_rtt_p50_ms`, `ack_rtt_p99_ms` in the JSON) is an upper bound.

## Findings that change the design

- **Windows monotonic clock is coarse.** Go's `time.Now()` monotonic reading on bigboi advances in about 0.5 ms steps and `time.Sleep` has a 0.5 ms floor. That collapsed 500 Mbps pacing to 25 Mbps and quantized every timestamp. `transport.Mono()` reads QueryPerformanceCounter on Windows (pure Go, via `syscall`); resolution is now under 10 µs.
- **Do not use connected UDP sockets on the host.** The Mac answered from its wired IP while the host had sent to its Wi-Fi IP, and Windows silently dropped replies on a connected socket. The sender now uses an unconnected socket; peers are authenticated by the AEAD key, not the source address.
- **Wi-Fi clients pay about 4 ms median and much fatter tails.** Raw net p50 4.4 to 8.4 ms on the Mac's Wi-Fi downlink vs 0.5 to 2.2 ms wired, and ICMP from bigboi to the idle Mac's Wi-Fi took 3 to 295 ms (power save). The client must keep the radio awake during a session, and the product should tell users that wired is best.
- **quic-go can drop datagrams on its own**: its receive queue holds 128 datagrams and discards the rest, and the send path drops a datagram if the packet size estimate shrinks. One clean wired run lost 0.03% of packets inside quic-go. Raw UDP lets Planck size its own socket buffer (8 MB here).
- **Reed-Solomon allocates inside the library**: about 2 allocations per encoded slice and 6 to 8 per reconstructed slice (pool boxing and index slices in klauspost/reedsolomon). Planck's own packetize, seal, open and reassemble path is 0 allocations per frame (unit-tested). If GC pressure matters at 240 fps, the codec needs a small fork or an allocation-free GF(2^8) path.

## Unit benchmark (Mac, M-series, 78 KB frame, 1152-byte shards)

```
BenchmarkSliceFEC/whole/fec0         	    3000	      6518 ns/op	11986.86 MB/s	     502 B/op	       0 allocs/op
BenchmarkSliceFEC/slice4/fec0        	    3000	      4149 ns/op	18828.45 MB/s	     579 B/op	       0 allocs/op
BenchmarkSliceFEC/slice4/fec20/noloss         	    3000	     12608 ns/op	6196.31 MB/s	    4360 B/op	       8 allocs/op
BenchmarkSliceFEC/slice4/fec20/loss6pct       	    3000	     15512 ns/op	5036.45 MB/s	   14463 B/op	      32 allocs/op
BenchmarkSliceFEC/whole/fec20/loss6pct        	    3000	     32216 ns/op	2425.03 MB/s	    9632 B/op	      10 allocs/op
```

Packetize plus reassemble only; no network. `fec20/loss6pct` drops every 17th packet.

## Wired LAN: congestion control matrix (bigboi to Mac `en7`, 1360-byte shards, no loss)

| transport | Mbps@fps | slices | min RTT | deliv % | p50 | p95 | p99 | net p50 | net p99 | 1st-slice p50 | jitter | send-slip p99 | offered→rx Mbps | tx allocs/frame | rx allocs/frame |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| quic-go dgram | 50@120 | 1 | 0.22 | 100.0 | 1.36 | 1.86 | 3.26 | 1.35 | 2.58 | 1.36 | 0.21 | 0.81 | 52→52 | 155.6 | 491.6 |
| raw burst | 50@120 | 1 | 0.20 | 100.0 | 1.24 | 1.74 | 7.45 | 1.24 | 3.67 | 1.24 | 0.79 | 3.63 | 52→52 | 0.1 | 0.2 |
| raw paced 500 | 50@120 | 1 | 0.23 | 100.0 | 1.28 | 1.48 | 3.40 | 1.28 | 3.21 | 1.28 | 0.24 | 0.22 | 52→52 | 0.1 | 0.2 |
| quic-go dgram | 50@120 | 4 | 0.22 | 100.0 | 1.89 | 4.50 | 18.65 | 1.87 | 8.61 | 1.31 | 0.67 | 10.44 | 53→53 | 169.9 | 502.0 |
| raw burst | 50@120 | 4 | 0.18 | 100.0 | 1.32 | 7.15 | 29.43 | 1.31 | 24.77 | 0.94 | 0.66 | 14.16 | 53→53 | 0.1 | 0.4 |
| raw paced 500 | 50@120 | 4 | 0.19 | 100.0 | 1.24 | 5.09 | 14.38 | 1.24 | 7.70 | 0.67 | 0.84 | 7.70 | 53→53 | 0.1 | 0.4 |
| quic-go dgram | 50@240 | 1 | 0.20 | 100.0 | 1.01 | 2.80 | 13.93 | 1.00 | 8.35 | 1.01 | 0.17 | 6.39 | 53→53 | 84.4 | 256.6 |
| raw burst | 50@240 | 1 | 0.23 | 100.0 | 0.48 | 2.87 | 7.58 | 0.48 | 5.28 | 0.48 | 0.32 | 2.97 | 53→53 | 0.0 | 0.1 |
| raw paced 500 | 50@240 | 1 | 0.19 | 100.0 | 0.68 | 2.99 | 9.51 | 0.68 | 4.84 | 0.68 | 0.09 | 5.89 | 53→53 | 0.0 | 0.1 |
| quic-go dgram | 50@240 | 4 | 0.24 | 100.0 | 0.68 | 2.87 | 22.58 | 0.67 | 5.03 | 0.54 | 0.36 | 15.85 | 53→53 | 81.6 | 254.4 |
| raw burst | 50@240 | 4 | 0.21 | 100.0 | 0.66 | 8.35 | 37.65 | 0.66 | 27.25 | 0.61 | 4.28 | 22.02 | 53→53 | 0.0 | 0.2 |
| raw paced 500 | 50@240 | 4 | 0.21 | 100.0 | 0.65 | 2.22 | 12.86 | 0.65 | 4.10 | 0.33 | 0.86 | 9.15 | 53→53 | 0.0 | 0.2 |
| quic-go dgram | 100@120 | 1 | 0.23 | 100.0 | 2.06 | 4.61 | 13.45 | 2.06 | 8.05 | 2.06 | 1.39 | 7.53 | 102→102 | 298.1 | 949.3 |
| raw burst | 100@120 | 1 | 0.17 | 100.0 | 1.97 | 2.31 | 5.92 | 1.96 | 5.88 | 1.97 | 0.40 | 2.00 | 102→102 | 0.2 | 0.2 |
| raw paced 500 | 100@120 | 1 | 0.21 | 100.0 | 2.07 | 3.59 | 14.18 | 2.06 | 6.83 | 2.07 | 0.13 | 7.27 | 102→102 | 0.2 | 0.2 |
| quic-go dgram | 100@120 | 4 | 0.22 | 100.0 | 2.52 | 6.28 | 25.40 | 2.49 | 13.96 | 1.89 | 0.96 | 13.56 | 106→106 | 309.3 | 992.7 |
| raw burst | 100@120 | 4 | 0.25 | 100.0 | 1.48 | 2.41 | 7.27 | 1.46 | 3.99 | 1.09 | 0.61 | 2.45 | 106→106 | 0.1 | 0.3 |
| raw paced 500 | 100@120 | 4 | 0.20 | 100.0 | 2.20 | 3.05 | 12.95 | 2.18 | 8.50 | 1.03 | 0.28 | 5.36 | 106→106 | 0.1 | 0.4 |
| quic-go dgram | 100@240 | 1 | 0.24 | 100.0 | 1.30 | 5.59 | 11.91 | 1.29 | 7.68 | 1.30 | 0.31 | 6.08 | 103→103 | 154.8 | 485.8 |
| raw burst | 100@240 | 1 | 0.23 | 100.0 | 0.84 | 4.38 | 26.79 | 0.83 | 7.92 | 0.84 | 0.43 | 17.12 | 103→103 | 0.0 | 0.1 |
| raw paced 500 | 100@240 | 1 | 0.19 | 100.0 | 1.13 | 5.24 | 18.72 | 1.12 | 7.74 | 1.13 | 0.27 | 11.12 | 103→103 | 0.1 | 0.1 |
| quic-go dgram | 100@240 | 4 | 0.20 | 100.0 | 1.27 | 4.27 | 14.16 | 1.26 | 6.23 | 0.98 | 0.56 | 8.13 | 106→106 | 159.1 | 499.3 |
| raw burst | 100@240 | 4 | 0.18 | 100.0 | 0.99 | 4.68 | 14.00 | 0.96 | 7.69 | 0.82 | 0.52 | 7.32 | 106→106 | 0.0 | 0.2 |
| raw paced 500 | 100@240 | 4 | 0.20 | 100.0 | 1.12 | 6.47 | 17.81 | 1.11 | 8.70 | 0.47 | 0.81 | 11.28 | 106→106 | 0.0 | 0.2 |
| quic-go dgram | 150@120 | 1 | 0.19 | 100.0 | 3.24 | 15.58 | 65.23 | 3.23 | 17.68 | 3.24 | 0.59 | 53.98 | 153→153 | 451.7 | 1422.9 |
| raw burst | 150@120 | 1 | 0.22 | 100.0 | 2.15 | 23.62 | 50.16 | 2.12 | 37.55 | 2.15 | 0.35 | 26.82 | 153→153 | 0.3 | 0.2 |
| raw paced 500 | 150@120 | 1 | 0.20 | 100.0 | 2.84 | 42.33 | 113.70 | 2.84 | 17.57 | 2.84 | 0.73 | 104.97 | 153→153 | 0.2 | 0.3 |
| quic-go dgram | 150@120 | 4 | 0.22 | 100.0 | 3.09 | 3.76 | 6.32 | 3.08 | 5.69 | 2.16 | 0.57 | 1.44 | 154→154 | 449.7 | 1443.1 |
| raw burst | 150@120 | 4 | 0.21 | 100.0 | 2.06 | 5.07 | 22.15 | 2.05 | 8.43 | 1.43 | 1.35 | 10.93 | 154→154 | 0.1 | 0.4 |
| raw paced 500 | 150@120 | 4 | 0.22 | 100.0 | 2.87 | 9.97 | 17.91 | 2.86 | 16.25 | 0.91 | 0.65 | 8.63 | 154→154 | 0.1 | 0.4 |
| quic-go dgram | 150@240 | 1 | 0.24 | 100.0 | 1.72 | 5.47 | 29.26 | 1.71 | 11.96 | 1.72 | 0.52 | 16.37 | 154→154 | 225.8 | 719.8 |
| raw burst | 150@240 | 1 | 0.19 | 100.0 | 1.30 | 7.49 | 26.92 | 1.25 | 10.27 | 1.30 | 0.27 | 19.60 | 154→154 | 0.1 | 0.1 |
| raw paced 500 | 150@240 | 1 | 0.21 | 100.0 | 1.59 | 6.61 | 26.64 | 1.56 | 7.83 | 1.59 | 0.84 | 21.09 | 154→154 | 0.1 | 0.1 |
| quic-go dgram | 150@240 | 4 | 0.22 | 100.0 | 1.80 | 4.42 | 8.70 | 1.79 | 5.79 | 1.25 | 0.39 | 2.90 | 159→159 | 232.8 | 747.3 |
| raw burst | 150@240 | 4 | 0.22 | 100.0 | 1.33 | 4.19 | 29.53 | 1.32 | 7.91 | 1.05 | 0.65 | 16.26 | 159→159 | 0.0 | 0.2 |
| raw paced 500 | 150@240 | 4 | 0.20 | 100.0 | 1.70 | 16.19 | 39.21 | 1.68 | 17.77 | 0.66 | 0.45 | 30.73 | 159→159 | 0.0 | 0.2 |

## Tailscale vs direct LAN (1100-byte shards, 4 slices, 240 fps, no loss)

| path | transport | Mbps@fps | min RTT | med RTT | deliv % | p50 | p95 | p99 | net p50 | net p99 | jitter | send-slip p99 | offered→rx Mbps | tx allocs/frame | rx allocs/frame |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| LAN | quic-go dgram | 50@240 | 0.25 | 0.36 | 100.0 | 1.11 | 54.46 | 89.28 | 1.10 | 41.83 | 0.180 | 60.90 | 52→52 | 97.8 | 304.5 |
| LAN | raw burst | 50@240 | 0.18 | 0.34 | 100.0 | 0.71 | 4.28 | 18.87 | 0.69 | 11.72 | 0.575 | 13.57 | 52→52 | 0.0 | 0.2 |
| LAN | raw paced 500 | 50@240 | 0.21 | 0.34 | 100.0 | 0.82 | 7.76 | 17.86 | 0.82 | 13.87 | 0.928 | 10.50 | 52→52 | 0.0 | 0.2 |
| Tailscale | quic-go dgram | 50@240 | 0.36 | 0.63 | 100.0 | 1.28 | 5.59 | 25.49 | 1.27 | 9.62 | 0.918 | 18.58 | 52→52 | 95.4 | 300.8 |
| Tailscale | raw burst | 50@240 | 0.36 | 0.59 | 100.0 | 0.98 | 6.66 | 36.17 | 0.97 | 11.06 | 0.189 | 26.70 | 52→52 | 0.0 | 0.2 |
| Tailscale | raw paced 500 | 50@240 | 0.40 | 0.80 | 100.0 | 0.95 | 5.77 | 17.75 | 0.94 | 9.46 | 0.398 | 8.74 | 52→52 | 0.0 | 0.2 |
| LAN | quic-go dgram | 100@240 | 0.22 | 0.40 | 100.0 | 1.40 | 3.94 | 12.09 | 1.39 | 6.44 | 0.502 | 6.29 | 103→103 | 187.7 | 597.8 |
| LAN | raw burst | 100@240 | 0.20 | 0.34 | 100.0 | 0.99 | 2.17 | 11.44 | 0.99 | 7.34 | 1.326 | 4.65 | 103→103 | 0.0 | 0.2 |
| LAN | raw paced 500 | 100@240 | 0.21 | 0.34 | 100.0 | 1.23 | 4.58 | 23.53 | 1.21 | 6.74 | 1.735 | 17.91 | 103→103 | 0.0 | 0.2 |
| Tailscale | quic-go dgram | 100@240 | 0.38 | 0.62 | 100.0 | 1.58 | 23.67 | 34.08 | 1.56 | 14.70 | 1.484 | 23.31 | 103→103 | 185.4 | 591.8 |
| Tailscale | raw burst | 100@240 | 0.37 | 0.59 | 100.0 | 4.07 | 20.93 | 41.15 | 3.15 | 23.36 | 2.844 | 33.28 | 103→103 | 0.0 | 0.2 |
| Tailscale | raw paced 500 | 100@240 | 0.33 | 0.56 | 100.0 | 4.79 | 33.66 | 58.09 | 3.97 | 22.57 | 2.354 | 52.49 | 103→103 | 0.0 | 0.2 |
| LAN | quic-go dgram | 150@240 | 0.21 | 0.38 | 100.0 | 2.07 | 3.65 | 25.89 | 2.06 | 9.51 | 0.268 | 15.48 | 155→155 | 275.8 | 890.1 |
| LAN | raw burst | 150@240 | 0.22 | 0.34 | 100.0 | 1.31 | 2.28 | 9.94 | 1.30 | 5.26 | 0.218 | 3.17 | 155→155 | 0.0 | 0.2 |
| LAN | raw paced 500 | 150@240 | 0.19 | 0.32 | 100.0 | 1.61 | 3.33 | 16.28 | 1.60 | 5.01 | 0.278 | 12.98 | 155→155 | 0.0 | 0.2 |
| Tailscale | quic-go dgram | 150@240 | 0.37 | 0.65 | 100.0 | 20.17 | 175.78 | 192.74 | 4.08 | 18.10 | 2.632 | 183.91 | 154→154 | 281.1 | 887.7 |
| Tailscale | raw burst | 150@240 | 0.36 | 0.58 | 100.0 | 1.59 | 35.41 | 47.37 | 1.53 | 20.77 | 2.211 | 38.84 | 155→155 | 0.0 | 0.2 |
| Tailscale | raw paced 500 | 150@240 | 0.36 | 0.61 | 100.0 | 635.84 | 1108.68 | 1126.76 | 8.16 | 25.15 | 4.114 | 1117.00 | 126→126 | 0.1 | 0.2 |

## Loss, jitter and FEC (wired LAN, 100 Mbps, 240 fps)

`parity %` is parity shards over data shards actually sent. `FEC recovery %` is slices rebuilt by Reed-Solomon over slices that needed it. `slices lost` could not be rebuilt.

| loss | transport | slices | FEC | parity % | obs. loss % | frames deliv % | FEC recovery % | slices lost | p50 | p99 | net p99 | 1st-slice p50 | tx allocs/frame | rx allocs/frame |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| 0% | quic-go dgram | 1 | 0 | 0.0 | 0.00 | 100.0 | n/a | 0 | 1.25 | 23.19 | 7.59 | 1.25 | 154.6 | 486.7 |
| 0% | raw paced 500 | 1 | 0 | 0.0 | 0.00 | 100.0 | n/a | 0 | 1.09 | 25.41 | 14.10 | 1.09 | 0.1 | 0.1 |
| 0% | quic-go dgram | 1 | 10 | 10.3 | 0.03 | 99.9 | 0.0 | 2 | 1.23 | 148.67 | 41.50 | 1.23 | 189.1 | 529.8 |
| 0% | raw paced 500 | 1 | 10 | 10.3 | 0.00 | 100.0 | n/a | 0 | 1.21 | 49.61 | 8.96 | 1.21 | 2.3 | 0.1 |
| 0% | quic-go dgram | 1 | 20 | 20.5 | 0.00 | 100.0 | n/a | 0 | 1.38 | 19.28 | 7.27 | 1.38 | 186.9 | 585.9 |
| 0% | raw paced 500 | 1 | 20 | 20.5 | 0.00 | 100.0 | n/a | 0 | 1.23 | 10.96 | 5.33 | 1.23 | 2.4 | 0.1 |
| 0% | quic-go dgram | 1 | adaptive | 2.6 | 0.00 | 100.0 | n/a | 0 | 1.27 | 14.45 | 5.81 | 1.27 | 167.0 | 499.8 |
| 0% | raw paced 500 | 1 | adaptive | 2.6 | 0.00 | 100.0 | n/a | 0 | 1.18 | 17.03 | 7.70 | 1.18 | 2.4 | 0.1 |
| 0% | quic-go dgram | 4 | 0 | 0.0 | 0.00 | 100.0 | n/a | 0 | 1.31 | 13.43 | 4.48 | 1.00 | 157.3 | 499.5 |
| 0% | raw paced 500 | 4 | 0 | 0.0 | 0.00 | 100.0 | n/a | 0 | 1.16 | 12.56 | 6.16 | 0.50 | 0.0 | 0.2 |
| 0% | quic-go dgram | 4 | 10 | 10.0 | 0.00 | 100.0 | n/a | 0 | 1.77 | 37.14 | 12.16 | 1.19 | 180.3 | 548.3 |
| 0% | raw paced 500 | 4 | 10 | 10.0 | 0.00 | 100.0 | n/a | 0 | 1.26 | 3.57 | 3.30 | 0.53 | 4.2 | 0.2 |
| 0% | quic-go dgram | 4 | 20 | 20.0 | 0.00 | 100.0 | n/a | 0 | 1.49 | 26.89 | 8.99 | 0.94 | 195.2 | 598.9 |
| 0% | raw paced 500 | 4 | 20 | 20.0 | 0.00 | 100.0 | n/a | 0 | 1.39 | 28.24 | 11.96 | 0.58 | 4.1 | 0.2 |
| 0% | quic-go dgram | 4 | adaptive | 10.0 | 0.00 | 100.0 | n/a | 0 | 1.40 | 114.47 | 24.96 | 1.03 | 180.7 | 548.2 |
| 0% | raw paced 500 | 4 | adaptive | 10.0 | 0.00 | 100.0 | n/a | 0 | 1.18 | 9.52 | 4.61 | 0.45 | 4.1 | 0.2 |
| 1% | quic-go dgram | 1 | 0 | 0.0 | 0.94 | 70.1 | 0.0 | 321 | 1.27 | 15.14 | 5.93 | 1.27 | 152.8 | 481.4 |
| 1% | raw paced 500 | 1 | 0 | 0.0 | 1.03 | 66.0 | 0.0 | 361 | 1.20 | 12.37 | 5.89 | 1.20 | 0.1 | 0.1 |
| 1% | quic-go dgram | 1 | 10 | 10.3 | 0.93 | 100.0 | 100.0 | 0 | 1.21 | 19.10 | 8.03 | 1.21 | 183.7 | 545.9 |
| 1% | raw paced 500 | 1 | 10 | 10.3 | 0.98 | 100.0 | 100.0 | 0 | 1.26 | 17.67 | 8.92 | 1.26 | 2.3 | 17.1 |
| 1% | quic-go dgram | 1 | 20 | 20.5 | 0.93 | 100.0 | 100.0 | 0 | 1.29 | 20.84 | 12.40 | 1.29 | 189.1 | 597.0 |
| 1% | raw paced 500 | 1 | 20 | 20.5 | 0.91 | 100.0 | 100.0 | 0 | 1.20 | 9.32 | 5.15 | 1.20 | 2.4 | 15.2 |
| 1% | quic-go dgram | 1 | adaptive | 8.5 | 1.00 | 100.0 | 99.2 | 3 | 1.32 | 9.42 | 4.54 | 1.32 | 168.3 | 545.7 |
| 1% | raw paced 500 | 1 | adaptive | 8.5 | 0.99 | 100.0 | 99.4 | 2 | 1.25 | 42.68 | 6.86 | 1.25 | 3.2 | 24.2 |
| 1% | quic-go dgram | 4 | 0 | 0.0 | 0.95 | 67.8 | 0.0 | 392 | 1.31 | 109.96 | 28.68 | 1.02 | 165.8 | 492.3 |
| 1% | raw paced 500 | 4 | 0 | 0.0 | 1.01 | 64.0 | 0.0 | 420 | 1.16 | 7.89 | 3.61 | 0.52 | 0.0 | 0.2 |
| 1% | quic-go dgram | 4 | 10 | 10.0 | 0.95 | 98.1 | 95.2 | 19 | 1.35 | 7.81 | 5.71 | 1.03 | 173.8 | 544.6 |
| 1% | raw paced 500 | 4 | 10 | 10.0 | 1.02 | 98.0 | 94.7 | 22 | 1.27 | 53.06 | 12.55 | 0.53 | 4.1 | 2.5 |
| 1% | quic-go dgram | 4 | 20 | 20.0 | 0.98 | 100.0 | 100.0 | 0 | 1.49 | 7.70 | 5.13 | 0.86 | 191.3 | 597.5 |
| 1% | raw paced 500 | 4 | 20 | 20.0 | 0.94 | 100.0 | 100.0 | 0 | 1.29 | 10.11 | 5.96 | 0.48 | 4.1 | 3.0 |
| 1% | quic-go dgram | 4 | adaptive | 19.8 | 1.02 | 100.0 | 99.8 | 1 | 1.41 | 67.50 | 8.59 | 0.98 | 207.3 | 592.4 |
| 1% | raw paced 500 | 4 | adaptive | 19.8 | 0.97 | 99.6 | 98.7 | 5 | 1.29 | 25.41 | 10.83 | 0.50 | 4.2 | 3.5 |
| 5% | quic-go dgram | 1 | 0 | 0.0 | 5.04 | 14.1 | 0.0 | 927 | 1.41 | 7.76 | 7.08 | 1.41 | 142.4 | 457.7 |
| 5% | raw paced 500 | 1 | 0 | 0.0 | 5.09 | 12.6 | 0.0 | 946 | 1.21 | 8.50 | 7.75 | 1.21 | 0.1 | 0.1 |
| 5% | quic-go dgram | 1 | 10 | 10.3 | 4.86 | 94.5 | 93.8 | 58 | 1.40 | 60.50 | 11.27 | 1.40 | 164.8 | 600.8 |
| 5% | raw paced 500 | 1 | 10 | 10.3 | 5.09 | 94.3 | 93.6 | 60 | 1.31 | 24.22 | 14.75 | 1.31 | 2.4 | 95.2 |
| 5% | quic-go dgram | 1 | 20 | 20.5 | 5.15 | 99.9 | 99.9 | 1 | 1.40 | 10.51 | 7.38 | 1.40 | 181.1 | 654.2 |
| 5% | raw paced 500 | 1 | 20 | 20.5 | 5.06 | 99.9 | 99.9 | 1 | 1.28 | 19.27 | 8.95 | 1.28 | 2.4 | 104.9 |
| 5% | quic-go dgram | 1 | adaptive | 20.4 | 5.21 | 100.0 | 98.7 | 12 | 1.40 | 9.58 | 6.57 | 1.40 | 180.0 | 673.5 |
| 5% | raw paced 500 | 1 | adaptive | 20.0 | 5.01 | 100.0 | 99.2 | 7 | 1.46 | 23.29 | 12.01 | 1.46 | 4.6 | 118.3 |
| 5% | quic-go dgram | 4 | 0 | 0.0 | 4.98 | 13.8 | 0.0 | 1714 | 1.38 | 41.04 | 29.52 | 1.01 | 158.0 | 469.1 |
| 5% | raw paced 500 | 4 | 0 | 0.0 | 5.11 | 11.4 | 0.0 | 1753 | 1.28 | 61.84 | 8.05 | 0.60 | 0.0 | 0.2 |
| 5% | quic-go dgram | 4 | 10 | 10.0 | 5.05 | 65.7 | 75.4 | 433 | 1.28 | 9.55 | 7.34 | 0.98 | 171.6 | 526.0 |
| 5% | raw paced 500 | 4 | 10 | 10.0 | 4.99 | 66.4 | 75.6 | 428 | 1.22 | 7.99 | 4.75 | 0.52 | 4.2 | 6.9 |
| 5% | quic-go dgram | 4 | 20 | 20.0 | 5.03 | 93.0 | 95.6 | 76 | 1.40 | 26.24 | 11.27 | 1.00 | 185.1 | 576.6 |
| 5% | raw paced 500 | 4 | 20 | 20.0 | 4.86 | 93.5 | 95.9 | 70 | 1.34 | 31.23 | 12.59 | 0.54 | 4.1 | 10.6 |
| 5% | quic-go dgram | 4 | adaptive | 39.6 | 5.01 | 99.9 | 99.4 | 10 | 1.49 | 7.08 | 4.40 | 0.99 | 209.8 | 674.5 |
| 5% | raw paced 500 | 4 | adaptive | 39.5 | 5.06 | 99.8 | 99.7 | 6 | 1.57 | 15.77 | 10.90 | 0.59 | 4.5 | 15.7 |
| 1% +2ms jit | quic-go dgram | 1 | 0 | 0.0 | 1.03 | 65.6 | 0.0 | 363 | 3.48 | 21.42 | 20.04 | 3.48 | 167.3 | 484.3 |
| 1% +2ms jit | raw paced 500 | 1 | 0 | 0.0 | 1.00 | 68.1 | 0.0 | 352 | 3.66 | 24.72 | 14.23 | 3.66 | 0.1 | 0.1 |
| 1% +2ms jit | quic-go dgram | 1 | 10 | 10.3 | 0.98 | 100.0 | 100.0 | 0 | 3.23 | 48.41 | 26.63 | 3.23 | 170.9 | 548.2 |
| 1% +2ms jit | raw paced 500 | 1 | 10 | 10.3 | 1.01 | 100.0 | 100.0 | 0 | 3.41 | 16.82 | 10.25 | 3.41 | 2.4 | 18.7 |
| 1% +2ms jit | quic-go dgram | 1 | 20 | 20.5 | 1.05 | 100.0 | 100.0 | 0 | 3.17 | 12.75 | 10.95 | 3.17 | 183.0 | 598.7 |
| 1% +2ms jit | raw paced 500 | 1 | 20 | 20.5 | 0.99 | 100.0 | 100.0 | 0 | 3.82 | 56.98 | 30.85 | 3.82 | 2.4 | 16.2 |
| 1% +2ms jit | quic-go dgram | 1 | adaptive | 8.7 | 1.02 | 100.0 | 100.0 | 0 | 3.35 | 233.13 | 227.78 | 3.35 | 181.0 | 548.0 |
| 1% +2ms jit | raw paced 500 | 1 | adaptive | 8.7 | 1.02 | 99.8 | 99.2 | 3 | 3.27 | 20.66 | 12.28 | 3.27 | 3.5 | 27.6 |
| 1% +2ms jit | quic-go dgram | 4 | 0 | 0.0 | 0.92 | 69.0 | 0.0 | 380 | 3.30 | 46.64 | 29.26 | 2.81 | 154.3 | 494.0 |
| 1% +2ms jit | raw paced 500 | 4 | 0 | 0.0 | 1.02 | 65.1 | 0.0 | 427 | 3.06 | 25.32 | 10.46 | 2.38 | 0.1 | 0.2 |
| 1% +2ms jit | quic-go dgram | 4 | 10 | 10.0 | 0.98 | 98.2 | 95.8 | 17 | 3.08 | 9.85 | 8.93 | 2.44 | 174.2 | 547.0 |
| 1% +2ms jit | raw paced 500 | 4 | 10 | 10.0 | 1.00 | 98.0 | 94.9 | 21 | 3.17 | 16.69 | 10.17 | 2.42 | 4.2 | 2.5 |
| 1% +2ms jit | quic-go dgram | 4 | 20 | 20.0 | 1.01 | 100.0 | 100.0 | 0 | 3.12 | 8.41 | 8.00 | 2.54 | 188.4 | 595.8 |
| 1% +2ms jit | raw paced 500 | 4 | 20 | 20.0 | 0.91 | 100.0 | 100.0 | 0 | 3.26 | 25.90 | 16.33 | 2.40 | 4.2 | 3.2 |
| 1% +2ms jit | quic-go dgram | 4 | adaptive | 19.8 | 1.01 | 99.8 | 99.5 | 2 | 3.14 | 28.85 | 16.27 | 2.52 | 191.3 | 594.1 |
| 1% +2ms jit | raw paced 500 | 4 | adaptive | 19.8 | 0.98 | 100.0 | 99.7 | 1 | 3.31 | 29.95 | 10.88 | 2.49 | 4.3 | 3.8 |

## Mac Wi-Fi downlink: congestion control matrix (bigboi to Mac `en0`)

The first pass targeted `192.168.188.138`, which is the Mac's Wi-Fi. Kept because a Wi-Fi client is a real deployment.

| transport | Mbps@fps | slices | min RTT | deliv % | p50 | p95 | p99 | net p50 | net p99 | 1st-slice p50 | jitter | send-slip p99 | offered→rx Mbps | tx allocs/frame | rx allocs/frame |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| quic-go dgram | 50@120 | 1 | 2.59 | 100.0 | 6.35 | 13.27 | 24.46 | 6.35 | 21.54 | 6.35 | 2.11 | 4.65 | 52→52 | 212.9 | 485.8 |
| raw burst | 50@120 | 1 | 2.19 | 100.0 | 5.28 | 10.04 | 22.05 | 5.27 | 16.17 | 5.28 | 2.42 | 7.66 | 52→52 | 0.1 | 0.2 |
| raw paced 500 | 50@120 | 1 | 2.27 | 100.0 | 5.75 | 10.73 | 22.12 | 5.70 | 17.44 | 5.75 | 4.06 | 9.00 | 52→52 | 0.1 | 0.3 |
| quic-go dgram | 50@120 | 4 | 2.67 | 100.0 | 7.30 | 107.19 | 169.88 | 7.26 | 54.93 | 5.90 | 1.42 | 151.14 | 53→53 | 393.9 | 500.3 |
| raw burst | 50@120 | 4 | 2.13 | 100.0 | 5.48 | 14.29 | 35.48 | 5.46 | 21.98 | 5.17 | 1.89 | 16.28 | 53→53 | 0.1 | 0.4 |
| raw paced 500 | 50@120 | 4 | 2.10 | 100.0 | 5.64 | 11.19 | 24.34 | 5.58 | 15.30 | 5.36 | 0.49 | 13.75 | 53→53 | 0.0 | 0.4 |
| quic-go dgram | 50@240 | 1 | 2.94 | 100.0 | 5.77 | 10.90 | 20.29 | 5.74 | 14.21 | 5.77 | 1.82 | 10.48 | 53→53 | 87.6 | 253.5 |
| raw burst | 50@240 | 1 | 2.23 | 100.0 | 4.41 | 9.76 | 17.88 | 4.36 | 14.22 | 4.41 | 1.58 | 8.16 | 53→53 | 0.0 | 0.1 |
| raw paced 500 | 50@240 | 1 | 2.14 | 100.0 | 4.49 | 7.72 | 10.91 | 4.40 | 10.19 | 4.49 | 1.87 | 2.12 | 53→53 | 0.0 | 0.1 |
| quic-go dgram | 50@240 | 4 | 2.88 | 100.0 | 6.07 | 10.68 | 18.36 | 6.04 | 13.83 | 4.38 | 1.61 | 5.02 | 53→53 | 84.0 | 253.9 |
| raw burst | 50@240 | 4 | 2.16 | 100.0 | 4.70 | 8.66 | 13.26 | 4.68 | 11.75 | 3.13 | 1.60 | 2.19 | 53→53 | 0.0 | 0.2 |
| raw paced 500 | 50@240 | 4 | 1.98 | 100.0 | 5.00 | 8.90 | 11.42 | 4.91 | 11.41 | 3.32 | 2.12 | 2.15 | 53→53 | 0.0 | 0.2 |
| quic-go dgram | 100@120 | 1 | 2.76 | 100.0 | 7.59 | 12.92 | 21.77 | 7.54 | 16.83 | 7.59 | 1.81 | 8.65 | 102→102 | 425.5 | 963.1 |
| raw burst | 100@120 | 1 | 2.24 | 100.0 | 5.65 | 9.64 | 19.44 | 5.60 | 11.53 | 5.65 | 1.10 | 4.98 | 102→102 | 0.2 | 0.2 |
| raw paced 500 | 100@120 | 1 | 2.16 | 100.0 | 23.54 | 109.04 | 195.74 | 8.11 | 126.87 | 23.54 | 14.23 | 170.50 | 99→99 | 0.2 | 0.3 |
| quic-go dgram | 100@120 | 4 | 2.78 | 100.0 | 8355.35 | 9162.14 | 9208.69 | 12.63 | 390.76 | 8346.91 | 4.56 | 9190.49 | 38→38 | 539.3 | 991.2 |
| raw burst | 100@120 | 4 | 2.23 | 100.0 | 5.56 | 10.37 | 20.05 | 5.43 | 17.44 | 4.98 | 1.27 | 10.77 | 106→106 | 0.1 | 0.4 |
| raw paced 500 | 100@120 | 4 | 2.16 | 100.0 | 6.87 | 9.32 | 12.18 | 6.84 | 11.75 | 5.28 | 0.79 | 2.22 | 106→106 | 0.1 | 0.5 |
| quic-go dgram | 100@240 | 1 | 2.70 | 100.0 | 5.70 | 14.86 | 33.29 | 5.65 | 22.79 | 5.70 | 0.88 | 18.98 | 103→103 | 160.3 | 475.9 |
| raw burst | 100@240 | 1 | 2.17 | 100.0 | 4.81 | 8.94 | 27.37 | 4.77 | 16.06 | 4.81 | 2.15 | 17.11 | 103→103 | 0.1 | 0.1 |
| raw paced 500 | 100@240 | 1 | 2.11 | 100.0 | 5.43 | 11.94 | 24.07 | 5.38 | 14.90 | 5.43 | 2.08 | 11.66 | 103→103 | 0.1 | 0.2 |
| quic-go dgram | 100@240 | 4 | 2.69 | 100.0 | 7.02 | 30.21 | 40.32 | 6.97 | 23.42 | 6.03 | 3.91 | 26.86 | 106→106 | 261.2 | 495.7 |
| raw burst | 100@240 | 4 | 2.12 | 100.0 | 5.66 | 11.38 | 21.09 | 5.54 | 14.67 | 4.59 | 1.57 | 9.00 | 106→106 | 0.0 | 0.2 |
| raw paced 500 | 100@240 | 4 | 2.16 | 100.0 | 5.67 | 11.02 | 26.12 | 5.65 | 14.66 | 4.55 | 4.50 | 11.51 | 106→105 | 0.0 | 0.2 |
| quic-go dgram | 150@120 | 1 | 2.94 | 100.0 | 8.95 | 16.68 | 45.55 | 8.79 | 20.75 | 8.95 | 1.32 | 27.15 | 153→152 | 588.7 | 1421.7 |
| raw burst | 150@120 | 1 | 2.16 | 100.0 | 7.44 | 12.03 | 28.79 | 7.37 | 19.13 | 7.44 | 1.27 | 11.89 | 153→152 | 0.2 | 0.2 |
| raw paced 500 | 150@120 | 1 | 2.01 | 100.0 | 8.76 | 22.44 | 38.21 | 8.39 | 26.86 | 8.76 | 1.79 | 19.55 | 152→152 | 0.2 | 0.3 |
| quic-go dgram | 150@120 | 4 | 2.83 | 100.0 | 11.35 | 25.75 | 36.20 | 11.20 | 22.71 | 7.97 | 2.87 | 20.06 | 154→154 | 734.1 | 1433.6 |
| raw burst | 150@120 | 4 | 2.18 | 100.0 | 7.83 | 12.10 | 25.27 | 7.71 | 15.36 | 6.70 | 1.48 | 9.55 | 154→154 | 0.1 | 0.4 |
| raw paced 500 | 150@120 | 4 | 2.23 | 100.0 | 7.83 | 12.75 | 19.96 | 7.78 | 17.29 | 6.06 | 2.10 | 7.10 | 154→154 | 0.1 | 0.4 |
| quic-go dgram | 150@240 | 1 | 2.98 | 100.0 | 566.02 | 704.87 | 713.00 | 11.48 | 48.46 | 566.02 | 2.09 | 700.79 | 143→143 | 439.3 | 717.3 |
| raw burst | 150@240 | 1 | 2.19 | 100.0 | 5.81 | 24.28 | 65.93 | 5.77 | 27.42 | 5.81 | 2.49 | 39.80 | 154→154 | 0.1 | 0.1 |
| raw paced 500 | 150@240 | 1 | 2.10 | 100.0 | 5.99 | 12.42 | 33.79 | 5.94 | 12.84 | 5.99 | 1.70 | 22.78 | 154→154 | 0.1 | 0.1 |
| quic-go dgram | 150@240 | 4 | 2.88 | 100.0 | 7.18 | 18.12 | 38.02 | 7.16 | 21.55 | 6.15 | 2.28 | 20.58 | 159→159 | 252.2 | 750.4 |
| raw burst | 150@240 | 4 | 2.17 | 100.0 | 6.88 | 12.51 | 20.03 | 6.55 | 16.67 | 5.64 | 2.58 | 5.80 | 159→159 | 0.0 | 0.2 |
| raw paced 500 | 150@240 | 4 | 2.20 | 100.0 | 7.94 | 17.29 | 27.63 | 7.42 | 20.95 | 6.27 | 2.90 | 14.22 | 159→159 | 0.0 | 0.2 |

The Wi-Fi pass of the FEC and Tailscale matrices is in `results/wifi-ts-fec.json`; its FEC results match the wired ones, with Wi-Fi latency added.

## Mac loopback baseline (software only, older build without the net columns)

| transport | Mbps@fps | slices | min RTT | deliv % | p50 | p95 | p99 | net p50 | net p99 | 1st-slice p50 | jitter | send-slip p99 | offered→rx Mbps | tx allocs/frame | rx allocs/frame |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| quic-go dgram | 50@120 | 1 | 0.10 | 100.0 | 1.20 | 1.37 | 1.53 | n/a | n/a | 1.20 | 0.29 | 0.79 | 52→52 | 276.9 | 490.9 |
| raw burst | 50@120 | 1 | 0.08 | 100.0 | 1.08 | 1.33 | 1.40 | n/a | n/a | 1.08 | 0.38 | 0.80 | 52→52 | 0.1 | 0.2 |
| raw paced 500 | 50@120 | 1 | 0.07 | 100.0 | 1.29 | 1.63 | 1.70 | n/a | n/a | 1.29 | 0.26 | 0.73 | 52→52 | 0.1 | 0.2 |
| quic-go dgram | 50@120 | 4 | 0.10 | 100.0 | 1.22 | 1.62 | 1.87 | n/a | n/a | 0.87 | 0.37 | 0.78 | 53→53 | 274.1 | 499.4 |
| raw burst | 50@120 | 4 | 0.10 | 100.0 | 1.20 | 1.52 | 1.97 | n/a | n/a | 0.85 | 0.38 | 0.79 | 53→53 | 0.1 | 0.4 |
| raw paced 500 | 50@120 | 4 | 0.07 | 100.0 | 1.58 | 1.71 | 1.84 | n/a | n/a | 0.90 | 0.27 | 0.73 | 53→53 | 0.1 | 0.4 |
| quic-go dgram | 50@240 | 1 | 0.13 | 100.0 | 0.56 | 0.87 | 1.31 | n/a | n/a | 0.56 | 0.10 | 0.35 | 53→53 | 140.8 | 252.4 |
| raw burst | 50@240 | 1 | 0.07 | 100.0 | 0.51 | 0.63 | 0.71 | n/a | n/a | 0.51 | 0.13 | 0.30 | 53→53 | 0.0 | 0.1 |
| raw paced 500 | 50@240 | 1 | 0.08 | 100.0 | 0.69 | 0.76 | 0.80 | n/a | n/a | 0.69 | 0.12 | 0.27 | 53→53 | 0.0 | 0.1 |
| quic-go dgram | 50@240 | 4 | 0.11 | 100.0 | 0.54 | 0.70 | 0.85 | n/a | n/a | 0.38 | 0.17 | 0.30 | 53→53 | 144.4 | 253.9 |
| raw burst | 50@240 | 4 | 0.06 | 100.0 | 0.57 | 0.73 | 0.94 | n/a | n/a | 0.39 | 0.09 | 0.30 | 53→53 | 0.0 | 0.2 |
| raw paced 500 | 50@240 | 4 | 0.08 | 100.0 | 0.70 | 2.40 | 5.28 | n/a | n/a | 0.36 | 0.18 | 2.72 | 53→53 | 0.0 | 0.2 |
| quic-go dgram | 100@120 | 1 | 0.08 | 100.0 | 1.61 | 2.06 | 2.37 | n/a | n/a | 1.61 | 0.43 | 0.74 | 102→102 | 520.9 | 950.5 |
| raw burst | 100@120 | 1 | 0.08 | 100.0 | 1.34 | 2.24 | 2.45 | n/a | n/a | 1.34 | 0.32 | 0.77 | 102→102 | 0.2 | 0.2 |
| raw paced 500 | 100@120 | 1 | 0.07 | 100.0 | 2.39 | 2.65 | 3.04 | n/a | n/a | 2.39 | 0.29 | 0.63 | 102→102 | 0.2 | 0.3 |
| quic-go dgram | 100@120 | 4 | 0.08 | 100.0 | 1.99 | 2.85 | 3.21 | n/a | n/a | 1.05 | 0.52 | 0.71 | 106→106 | 542.0 | 985.1 |
| raw burst | 100@120 | 4 | 0.09 | 100.0 | 1.71 | 2.41 | 2.68 | n/a | n/a | 0.87 | 0.37 | 0.76 | 106→106 | 0.1 | 0.4 |
| raw paced 500 | 100@120 | 4 | 0.07 | 100.0 | 2.40 | 2.64 | 2.93 | n/a | n/a | 1.03 | 0.29 | 0.64 | 106→106 | 0.1 | 0.4 |
| quic-go dgram | 100@240 | 1 | 0.10 | 100.0 | 0.89 | 1.30 | 1.53 | n/a | n/a | 0.89 | 0.13 | 0.27 | 103→103 | 265.7 | 482.3 |
| raw burst | 100@240 | 1 | 0.07 | 100.0 | 0.79 | 1.18 | 1.46 | n/a | n/a | 0.79 | 0.16 | 0.28 | 103→103 | 0.1 | 0.1 |
| raw paced 500 | 100@240 | 1 | 0.06 | 100.0 | 1.08 | 1.14 | 1.16 | n/a | n/a | 1.08 | 0.08 | 0.22 | 103→103 | 0.1 | 0.1 |
| quic-go dgram | 100@240 | 4 | 0.09 | 100.0 | 0.76 | 0.95 | 1.11 | n/a | n/a | 0.42 | 0.14 | 0.27 | 106→106 | 275.6 | 496.4 |
| raw burst | 100@240 | 4 | 0.08 | 100.0 | 0.67 | 0.83 | 0.88 | n/a | n/a | 0.37 | 0.10 | 0.27 | 106→106 | 0.0 | 0.2 |
| raw paced 500 | 100@240 | 4 | 0.08 | 100.0 | 1.09 | 1.13 | 1.16 | n/a | n/a | 0.41 | 0.08 | 0.21 | 106→106 | 0.0 | 0.2 |
| quic-go dgram | 150@120 | 1 | 0.10 | 100.0 | 2.09 | 2.64 | 2.82 | n/a | n/a | 2.09 | 0.23 | 0.67 | 153→153 | 773.3 | 1406.3 |
| raw burst | 150@120 | 1 | 0.08 | 100.0 | 2.01 | 2.38 | 2.74 | n/a | n/a | 2.01 | 0.42 | 0.70 | 153→153 | 0.3 | 0.2 |
| raw paced 500 | 150@120 | 1 | 0.08 | 100.0 | 3.07 | 3.14 | 3.25 | n/a | n/a | 3.07 | 0.15 | 0.51 | 153→153 | 0.3 | 0.2 |
| quic-go dgram | 150@120 | 4 | 0.09 | 100.0 | 2.32 | 2.66 | 2.79 | n/a | n/a | 1.03 | 0.26 | 0.63 | 154→154 | 780.3 | 1419.2 |
| raw burst | 150@120 | 4 | 0.05 | 100.0 | 1.99 | 2.37 | 2.73 | n/a | n/a | 0.99 | 0.35 | 0.72 | 154→154 | 0.1 | 0.4 |
| raw paced 500 | 150@120 | 4 | 0.08 | 100.0 | 3.09 | 3.15 | 3.18 | n/a | n/a | 1.12 | 0.19 | 0.51 | 154→154 | 0.1 | 0.4 |
| quic-go dgram | 150@240 | 1 | 0.10 | 100.0 | 1.08 | 1.37 | 1.61 | n/a | n/a | 1.08 | 0.13 | 0.23 | 154→154 | 389.3 | 713.0 |
| raw burst | 150@240 | 1 | 0.07 | 100.0 | 0.87 | 1.18 | 1.51 | n/a | n/a | 0.87 | 0.06 | 0.26 | 154→154 | 0.1 | 0.1 |
| raw paced 500 | 150@240 | 1 | 0.08 | 100.0 | 1.47 | 1.53 | 1.58 | n/a | n/a | 1.47 | 0.05 | 0.16 | 154→154 | 0.1 | 0.1 |
| quic-go dgram | 150@240 | 4 | 0.12 | 100.0 | 0.93 | 1.38 | 1.69 | n/a | n/a | 0.43 | 0.12 | 0.24 | 159→159 | 401.2 | 736.3 |
| raw burst | 150@240 | 4 | 0.09 | 100.0 | 0.78 | 1.46 | 1.64 | n/a | n/a | 0.38 | 0.18 | 0.26 | 159→159 | 0.0 | 0.2 |
| raw paced 500 | 150@240 | 4 | 0.08 | 100.0 | 1.54 | 1.65 | 1.76 | n/a | n/a | 0.52 | 0.07 | 0.16 | 159→159 | 0.0 | 0.2 |

## Reproduce

```sh
# client (Mac)
CGO_ENABLED=0 go build -o spike-transport ./cmd/spike-transport
./spike-transport recv -listen 0.0.0.0:47998 -media-port 47999

# host (Windows)
CGO_ENABLED=0 go build -o spike-transport.exe ./cmd/spike-transport
./spike-transport.exe send -server 192.168.188.165:47998 -ts-server 100.74.237.90:47998 -matrix cc,ts,fec -out wired.json
```

Raw results: `results/wired-cc-ts-fec.json` (118 runs: 36 CC, 18 Tailscale/LAN, 64 FEC), `results/wifi-cc.json`, `results/wifi-ts-fec.json`, `results/mac-loopback-cc.json`.

## Not covered

- Real encoder output (variable frame sizes, keyframe bursts); frames here are constant-size.
- Bursty (Gilbert-Elliott) loss; injected loss is independent per packet.
- Planck's own rate control; this spike runs at fixed bitrates.
- Certificate pinning: the spike client skips TLS verification. The real client must pin the host certificate learned at pairing.
- Linux hosts and AMD/Intel NICs.
