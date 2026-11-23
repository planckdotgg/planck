| from → to | size | format | method | p50 | p95 | p99 | max | breakdown | allocs/frame |
|---|---|---|---|---|---|---|---|---|---|
| AMD Radeon RX 6700 XT (same GPU) | 1920x1080 | bgra | same-GPU CopyResource | 0.075 | 0.157 | 0.317 | 3.498 |  | 0.0 |
| AMD Radeon RX 6700 XT → NVIDIA GeForce RTX 4080 | 1920x1080 | bgra | staging readback + upload | 3.969 | 4.148 | 4.419 | 8.184 | readback p50 0.894 / CPU copy p50 0.523 / upload p50 2.543 | 0.0 |
| AMD Radeon RX 6700 XT (same GPU) | 1920x1080 | nv12 | same-GPU CopyResource | 0.092 | 0.197 | 0.226 | 1.509 |  | 0.0 |
| AMD Radeon RX 6700 XT → NVIDIA GeForce RTX 4080 | 1920x1080 | nv12 | staging readback + upload | 1.728 | 1.814 | 2.106 | 3.875 | readback p50 0.399 / CPU copy p50 0.207 / upload p50 1.116 | 0.0 |
| AMD Radeon RX 6700 XT (same GPU) | 2560x1440 | bgra | same-GPU CopyResource | 0.083 | 0.205 | 0.252 | 4.868 |  | 0.0 |
| AMD Radeon RX 6700 XT → NVIDIA GeForce RTX 4080 | 2560x1440 | bgra | staging readback + upload | 15.440 | 17.782 | 30.991 | 31.970 | readback p50 1.517 / CPU copy p50 0.959 / upload p50 12.927 | 0.0 |
| AMD Radeon RX 6700 XT (same GPU) | 2560x1440 | nv12 | same-GPU CopyResource | 0.178 | 0.723 | 2.393 | 2.816 |  | 0.0 |
| AMD Radeon RX 6700 XT → NVIDIA GeForce RTX 4080 | 2560x1440 | nv12 | staging readback + upload | 15.109 | 16.094 | 16.617 | 46.280 | readback p50 0.541 / CPU copy p50 0.370 / upload p50 13.966 | 0.0 |
