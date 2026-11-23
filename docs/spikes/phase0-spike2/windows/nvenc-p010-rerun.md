# NVENC P010 and RGB10 input, rerun after encode.Config.Source carried the source bit depth

The matrices above ran before the encoder knew the source bit depth, so P010
input failed at nvEncRegisterResource (NV_ENC_ERR_INVALID_PARAM). These rows
are the same cases after the fix, 1200 measured frames each.

| pace | codec | chroma | input | res | fps | slices | subframe | IR | split | encode p50 | p95 | p99 | max | first slice | kbps out | IDR bytes | allocs/frame |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| paced | hevc | p010 | p010 | 1920x1080 | 120 | 0 | false | 0 | 0 | 2.951 | 6.980 | 12.092 | 37.008 | - | 46402 | 50354 | 0.0 |
| paced | hevc | p010 | rgb10 | 1920x1080 | 120 | 0 | false | 0 | 0 | 3.264 | 4.502 | 6.210 | 30.681 | - | 25995 | 50214 | 0.0 |
| paced | av1 | p010 | p010 | 1920x1080 | 120 | 0 | false | 0 | 0 | 2.975 | 4.216 | 8.989 | 50.078 | - | 43920 | 72872 | 0.0 |
| paced | hevc | yuv444p10 | rgb10 | 1920x1080 | 120 | 0 | false | 0 | 0 | 4.242 | 5.864 | 7.880 | 34.249 | - | 15006 | 50423 | 0.0 |
| paced | hevc | p010 | p010 | 2560x1440 | 120 | 0 | false | 0 | 0 | 4.328 | 6.169 | 9.458 | 26.838 | - | 80597 | 89744 | 0.0 |
| paced | av1 | p010 | p010 | 2560x1440 | 120 | 0 | false | 0 | 0 | 4.667 | 7.024 | 10.977 | 28.309 | - | 74146 | 140796 | 0.0 |
| back-to-back | hevc | p010 | p010 | 1920x1080 | 120 | 0 | false | 0 | 0 | 1.341 | 1.457 | 1.584 | 3.526 | - | 46402 | 50354 | 0.0 |
| back-to-back | hevc | p010 | rgb10 | 1920x1080 | 120 | 0 | false | 0 | 0 | 1.394 | 1.468 | 1.589 | 6.297 | - | 25995 | 50214 | 0.0 |
| back-to-back | av1 | p010 | p010 | 1920x1080 | 120 | 0 | false | 0 | 0 | 1.337 | 1.391 | 1.579 | 4.460 | - | 43920 | 72872 | 0.0 |
| back-to-back | hevc | yuv444p10 | rgb10 | 1920x1080 | 120 | 0 | false | 0 | 0 | 2.070 | 2.157 | 2.436 | 5.515 | - | 15006 | 50423 | 0.0 |
| back-to-back | hevc | p010 | p010 | 2560x1440 | 120 | 0 | false | 0 | 0 | 2.185 | 2.258 | 2.876 | 19.064 | - | 80597 | 89744 | 0.0 |
| back-to-back | av1 | p010 | p010 | 2560x1440 | 120 | 0 | false | 0 | 0 | 2.291 | 2.395 | 3.274 | 26.813 | - | 74146 | 140796 | 0.0 |
