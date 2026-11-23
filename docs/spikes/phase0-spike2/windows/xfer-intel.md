| from → to | size | format | method | p50 | p95 | p99 | max | breakdown | allocs/frame |
|---|---|---|---|---|---|---|---|---|---|
| Intel(R) UHD Graphics 770 (same GPU) | 1920x1080 | bgra | same-GPU CopyResource | 0.341 | 0.410 | 0.579 | 1.983 |  | 0.0 |
| Intel(R) UHD Graphics 770 → NVIDIA GeForce RTX 4080 | 1920x1080 | bgra | staging readback + upload | 4.323 | 4.439 | 4.684 | 6.917 | readback p50 1.536 / CPU copy p50 0.229 / upload p50 2.551 | 0.0 |
