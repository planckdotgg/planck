# \\.\DISPLAY1 1920x1080 @ 60 Hz → encode

| path | capture copy p50 | transfer p50/p99 | encode p50/p99 | present → encoded p50 | p95 | p99 | max | n |
|---|---|---|---|---|---|---|---|---|
| DDA (AMD) → AMF (same GPU, zero copy) | 0.101 | 0.000 / 0.000 | 3.321 / 4.277 | 3.579 | 3.964 | 5.965 | 13.549 | 600 |
| DDA (AMD) → staging → NVENC (RTX 4080) | 0.106 | 1.545 / 2.382 | 7.586 / 14.109 | 9.369 | 14.765 | 17.115 | 19.908 | 600 |
