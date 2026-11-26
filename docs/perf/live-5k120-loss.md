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
