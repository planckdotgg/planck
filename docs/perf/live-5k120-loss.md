# 5120x1440@120 over wired 2.5GbE: loss, recovery and present

Measured 2026-11-26, bigboi (RTX 4080, Destiny 2 running) to the M5 Pro
(Thunderbolt Intel I225, DriverKit `AppleEthernetE1000`, en7 at 2500Base-T,
flow control on). Live session being explained: glass-to-glass p50 13.7 /
p99 63.8 ms, present 5.29 ms p50, 0.29% loss, 18 unrepaired frames, two
"5 frames back, past 4" keyframe fallbacks.

## Loss happens below the Mac's socket, in long runs

A throwaway UDP burst tester (1452-byte datagrams, 120 frames a second,
spin-paced on QueryPerformanceCounter) sent frames bigboi to Mac:

| Frames | Pacing | Lost runs (consecutive datagrams) |
|---|---|---|
| 150 KB, 4 x 5 s | line rate to 1 Gbit/s | none |
| 300-1000 KB, 12 runs of 5-10 s | line rate to 1 Gbit/s | 0-7 runs per test, 29-232 long, also at 1 Gbit/s |
| 130 KB with a 500 KB frame every 20th, 7 x 20 s | line rate to 0.8 Gbit/s | one run of 46 and one lone datagram in 140 s |
| same, Mac on 18 busy cores, 4 x 20 s | 2.0 and 0.8 | one run of 213 |

Mac to bigboi, 600 KB at 2 Gbit/s and 1 MB frames unpaced: 0 of 1,321,200
lost. During every run the Mac's `dropped due to full socket buffers`
(netstat -s -p udp) stayed at 58505, en7 had 0 input errors, and bigboi's
NIC 0 outbound discards. The 8 MiB socket buffers were granted on both
ends. The Mac's receive path (the DriverKit NIC driver, or the switch
port into it) drops bursts of 50-230 datagrams now and then; the
receiving application never sees them. The burst floor (6 parity for a
64+ shard P frame) cannot repair such a run, so each one loses a frame,
and recovery goes through reference invalidation.

## Reference invalidation was a frame late

The video loop applied invalidations before it waited for the display,
so a RefInvalidate arriving during the wait reached the encoder a frame
later. refDPB 5 cannot grow at this size: 5120x1440 is over 3/4 of HEVC
Level 5.x MaxLumaPs, where the DPB holds 6 pictures, the current one
included. Fixed by applying invalidations between capture and encode
(`sourcePipe` gate, `TestInvalidationDuringCaptureWait`).

## Present on macOS

`TestLiveDirectGlass` (5120x1440@120, 8-bit HEVC, 1200 frames a phase):

| Run | Phase | Present call p50 / p99 | Present to glass p50 / p99 |
|---|---|---|---|
| 2 drawables, CuaDriver running | direct | 0.11 / 8.90 | 5.32 / 47.19 |
| 3 drawables, CuaDriver running | direct | 0.07 / 8.46 | 4.13 / 31.96 |
| 3 drawables, CuaDriver stopped | direct | 0.08 / 2.00 | 3.79 / 34.66 |

The composited phase, unchanged between runs, moved 1 ms (5.59, 4.64,
4.12), so one run of each is within noise. In direct mode, flips land on
a refresh even with `displaySyncEnabled` off: present to glass is half a
refresh plus about 1 ms. The bare player does not wait for drawables
(0.1 ms), so the 5.29 ms present call of the GUI session is not
reproduced here. Stopping CuaDriver leaves the 32-48 ms glass p99 in
place. The drawable count stays 2.

Next measurement: the GUI client with `PLANCK_TRACE_CLIENT`, with the
stats panel open and closed (open keeps the surface composited), to see
which present calls wait.

## Network, 1.5 ms p50 on a 0.43 ms round trip

Headless, desktop at 34 fps, frames about 111 KB: first datagram 0.37
ms p50 after the host's encode end, last 1.12 ms p50 after the first.
Windows `WriteToUDP` costs 3.8-6 us a datagram in the burst tester, so a
frame's 80 datagrams take about 0.4 ms of sender time on top of pacing.
UDP send offload (one call per train) is the candidate.

## Follow-up: send offload, receive-queue pacing, GUI present

Measured 2026-11-26, branch `perf/gso-pacing-present`, bigboi to the M5
Pro over en7 (192.168.188.165). Destiny 2 was not running; the Mac's
load average was 5 to 11 from other work.

### UDP send offload on Windows

Video datagrams are sealed into a train and sent with one `WSASendMsg`
carrying `UDP_SEND_MSG_SIZE` (`pkg/transport/gso.go`). bigboi's I225-V
has no UDP segmentation offload, so Windows cuts the train in software.
`TestLiveBurst` (2 Gbit/s pacing, 16-datagram bursts, 600 to 2400 frames
a case), time in send calls per frame and first-to-last datagram sent:

| Frame | Send calls p50 / p99, per datagram | Offload send calls p50 / p99 | First to last sent p99, per datagram / offload |
|---|---|---|---|
| 150 KB | 0.352 / 1.221 ms | 0.137 / 0.301 ms | 1.740 / 0.674 ms |
| 500 KB | 1.073-1.084 / 4.537-6.822 ms | 0.381-0.383 / 1.553-2.046 ms | 5.992-7.388 / 3.869-4.815 ms |
| 1 MB | 2.098 / 6.883 ms | 0.719 / 2.476 ms | 8.712 / 7.021 ms |

A datagram costs 1.3 us instead of 3.4 us. The p50 first-to-last spread
does not change (0.53 ms for 150 KB): at 2 Gbit/s it is set by pacing.
The win is sender CPU time and the tail. Offload is used only when
`getsockopt(UDP_SEND_MSG_SIZE)` succeeds; a train that fails with it and
then goes out one datagram at a time turns it off for the session. Linux
`UDP_SEGMENT` takes the same train with a different control message and
is left for a measured Linux host.

### Where the large-frame loss comes from

`TestLiveBurst` logs each lossy frame's missing datagrams. They start at
datagram 127 to 158 of the frame, almost never earlier, whatever the
frame size: the Mac's path below the socket holds about 128 datagrams
and drops what comes after while its drain is behind. Many lossy frames
show no silence over 0.2 ms at the socket, so it is not only full
stalls. The Mac's counters stay flat (`full socket buffers` 58505, en7
input errors 0); without root, Skywalk's ring statistics cannot be read.

What did not matter, 20-40 s a case:

- Burst shape: pacing bursts of 4, 16 and 64 datagrams lost 5, 1 and
  12 of 2400 500 KB frames, and offload against one call a datagram
  went 1 against 8 in one run and 5 against 0 in another.
- A 64-byte datagram every 250 us between frames, to keep the receive
  path awake: 240 KB frames still lost 5 of 4800.

What did: frames under the queue, and the arrival rate of frames over
it. 170 KB frames (121 datagrams) lost 0 of 4800 at 2 Gbit/s; 240 KB
frames lost 6, 3, 1 and 0 of 4800 at 2000, 1000, 700 and 400 Mbit/s;
300 KB frames 4, 4 and 0 at 2000, 700 and 500. Sequential cases drift
with the Mac's load (one run lost 150 KB frames to stalls longer than a
frame interval), so the fix was checked with alternate frames of one
case paced each way, 5400 frames per arm:

| Frame | 2 Gbit/s: lossy frames, lost datagrams | Receive-queue pacing | Arrival spread p50 |
|---|---|---|---|
| 200 KB | 2, 17 | 1, 3 | 1.82 / 2.18 ms |
| 240 KB | 29, 701 | 0, 0 | 2.11 / 3.89 ms |
| 300 KB | 13, 1183 | 4, 113 | 2.76 / 6.62 ms |

### Receive-queue pacing

`Pacing.Queue` spreads a frame bigger than the receiver's queue so its
excess arrives no faster than the queue drains; frames that fit keep the
LAN rate. The host sets `MacReceiveQueue` (112 datagrams of 1452 bytes,
175 Mbit/s drain) for macOS clients on a LAN. NVENC already runs a
one-frame VBV, so frame size follows bitrate: at 120 fps a frame fits
the queue up to about 140 Mbit/s, and 5120x1440 at the 212-223 Mbit/s
of the live sessions does not.

Headless client against a `-synth` 5120x1440@120 HEVC host at 212
Mbit/s, 40 s each (the synthetic stream runs at 115 Mbit/s, one frame in
eight about 277 KB):

| Host | Frames lost | Network one-way p50 / p95 / p99 | E2E p50 / p95 / p99 | Host encode end to last datagram, frames over the queue p50 |
|---|---|---|---|---|
| main 0c3a126 | 1 of 4688 | 1.32 / 3.08 / 3.51 ms | 5.85 / 9.90 / 10.51 ms | 1.20 ms |
| this branch | 0 of 4695 | 1.08 / 6.11 / 6.41 ms | 5.68 / 13.04 / 14.07 ms | 5.88 ms |

The p50 does not regress; frames over the queue arrive about 4.7 ms
later, which is the p95 here. Each of those frames would otherwise
carry a chance of losing its tail, which FEC cannot repair: the queue
keeps the first 128 datagrams, so a frame of more than 128 data shards
loses data, not only parity. A retransmission request for a lost tail
would remove the trade (a stall plus one 0.4 ms round trip on the frames
that lose datagrams, nothing on the rest); it needs a protocol message
and is not in this change.

### GUI present

The real client (`/Applications/Planck.app`, main 0c3a126, a second
instance with `PLANCK_TRACE_CLIENT`), full screen 5120x1440, HEVC 10-bit,
64 fps of a color-cycling window on the host:

| Phase | Frames | Present call p50 / p95 / p99 | Present start to glass p50 / p95 / p99 |
|---|---|---|---|
| Metrics closed (direct) | 15009 | 0.12 / 0.54 / 16.62 ms | 5.02 / 16.36 / 54.53 ms |
| Metrics open (composited) | 5484 | 0.12 / 13.76 / 24.76 ms | 12.80 / 24.08 / 57.18 ms |

The GUI's "Present" stage shows present-to-glass whenever glass times
exist (`cmd/planck-client/backend.go`, `telemetry`), not the present
call. The 5.29 ms of the live session is the scan-out wait of direct
mode, as `TestLiveDirectGlass` measured; the call itself does not wait at
p50 in either mode. Opening the metrics window makes the surface
composited and adds about one refresh (7.8 ms p50) to the latency it
shows; drawing the metrics into a direct surface, as the resting button
is, would remove that. No GUI-side wait to fix was found, and
`third_party/mygo` is unchanged.

Separately: a client started hidden (`open -g`) stayed on "Connecting"
with the host streaming until its window was brought forward.
