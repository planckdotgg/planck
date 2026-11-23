| from → to | size | format | method | p50 | p95 | p99 | max | breakdown | allocs/frame |
|---|---|---|---|---|---|---|---|---|---|
| NVIDIA GeForce RTX 4080 (same GPU) | 1920x1080 | bgra | same-GPU CopyResource | 0.028 | 0.064 | 0.091 | 1.422 |  | 0.0 |
| NVIDIA GeForce RTX 4080 → AMD Radeon RX 6700 XT | 1920x1080 | bgra | shared NT handle | failed: open on AMD Radeon RX 6700 XT: HRESULT 0x80070057 |||||| 
| NVIDIA GeForce RTX 4080 → AMD Radeon RX 6700 XT | 1920x1080 | bgra | shared legacy handle | failed: open on AMD Radeon RX 6700 XT: HRESULT 0x80070057 |||||| 
| NVIDIA GeForce RTX 4080 → AMD Radeon RX 6700 XT | 1920x1080 | bgra | staging readback + upload | 3.938 | 4.074 | 4.187 | 10.131 | readback p50 2.568 / CPU copy p50 0.530 / upload p50 0.836 | 0.0 |
| NVIDIA GeForce RTX 4080 → Intel(R) UHD Graphics 770 | 1920x1080 | bgra | shared NT handle | failed: open on Intel(R) UHD Graphics 770: HRESULT 0x80070057 |||||| 
| NVIDIA GeForce RTX 4080 → Intel(R) UHD Graphics 770 | 1920x1080 | bgra | shared legacy handle | failed: open on Intel(R) UHD Graphics 770: HRESULT 0x80070057 |||||| 
| NVIDIA GeForce RTX 4080 → Intel(R) UHD Graphics 770 | 1920x1080 | bgra | staging readback + upload | 3.477 | 3.693 | 4.188 | 9.630 | readback p50 2.573 / CPU copy p50 0.536 / upload p50 0.352 | 0.0 |
| NVIDIA GeForce RTX 4080 (same GPU) | 1920x1080 | nv12 | same-GPU CopyResource | 0.029 | 0.032 | 0.071 | 1.352 |  | 0.0 |
| NVIDIA GeForce RTX 4080 → AMD Radeon RX 6700 XT | 1920x1080 | nv12 | shared NT handle | failed: open on AMD Radeon RX 6700 XT: HRESULT 0x80070057 |||||| 
| NVIDIA GeForce RTX 4080 → AMD Radeon RX 6700 XT | 1920x1080 | nv12 | shared legacy handle | failed: open on AMD Radeon RX 6700 XT: HRESULT 0x80070057 |||||| 
| NVIDIA GeForce RTX 4080 → AMD Radeon RX 6700 XT | 1920x1080 | nv12 | staging readback + upload | 2.263 | 4.172 | 7.682 | 13.484 | readback p50 1.232 / CPU copy p50 0.486 / upload p50 0.406 | 0.0 |
| NVIDIA GeForce RTX 4080 → Intel(R) UHD Graphics 770 | 1920x1080 | nv12 | shared NT handle | failed: open on Intel(R) UHD Graphics 770: HRESULT 0x80070057 |||||| 
| NVIDIA GeForce RTX 4080 → Intel(R) UHD Graphics 770 | 1920x1080 | nv12 | shared legacy handle | failed: open on Intel(R) UHD Graphics 770: HRESULT 0x80070057 |||||| 
| NVIDIA GeForce RTX 4080 → Intel(R) UHD Graphics 770 | 1920x1080 | nv12 | staging readback + upload | 6.033 | 12.573 | 18.118 | 197.345 | readback p50 1.327 / CPU copy p50 1.700 / upload p50 2.308 | 0.0 |
| NVIDIA GeForce RTX 4080 (same GPU) | 1920x1080 | p010 | same-GPU CopyResource | 0.078 | 0.141 | 0.318 | 2.979 |  | 0.0 |
| NVIDIA GeForce RTX 4080 → AMD Radeon RX 6700 XT | 1920x1080 | p010 | shared NT handle | failed: open on AMD Radeon RX 6700 XT: HRESULT 0x80070057 |||||| 
| NVIDIA GeForce RTX 4080 → AMD Radeon RX 6700 XT | 1920x1080 | p010 | shared legacy handle | failed: open on AMD Radeon RX 6700 XT: HRESULT 0x80070057 |||||| 
| NVIDIA GeForce RTX 4080 → AMD Radeon RX 6700 XT | 1920x1080 | p010 | staging readback + upload | 3.668 | 8.338 | 11.941 | 38.029 | readback p50 2.208 / CPU copy p50 0.930 / upload p50 0.429 | 0.0 |
| NVIDIA GeForce RTX 4080 → Intel(R) UHD Graphics 770 | 1920x1080 | p010 | shared NT handle | failed: open on Intel(R) UHD Graphics 770: HRESULT 0x80070057 |||||| 
| NVIDIA GeForce RTX 4080 → Intel(R) UHD Graphics 770 | 1920x1080 | p010 | shared legacy handle | failed: open on Intel(R) UHD Graphics 770: HRESULT 0x80070057 |||||| 
| NVIDIA GeForce RTX 4080 → Intel(R) UHD Graphics 770 | 1920x1080 | p010 | staging readback + upload | 7.706 | 14.511 | 20.453 | 198.167 | readback p50 2.237 / CPU copy p50 2.202 / upload p50 1.534 | 0.0 |
| NVIDIA GeForce RTX 4080 (same GPU) | 2560x1440 | bgra | same-GPU CopyResource | 0.115 | 0.364 | 1.728 | 4.725 |  | 0.0 |
| NVIDIA GeForce RTX 4080 → AMD Radeon RX 6700 XT | 2560x1440 | bgra | shared NT handle | failed: open on AMD Radeon RX 6700 XT: HRESULT 0x80070057 |||||| 
| NVIDIA GeForce RTX 4080 → AMD Radeon RX 6700 XT | 2560x1440 | bgra | shared legacy handle | failed: open on AMD Radeon RX 6700 XT: HRESULT 0x80070057 |||||| 
| NVIDIA GeForce RTX 4080 → AMD Radeon RX 6700 XT | 2560x1440 | bgra | staging readback + upload | 9.707 | 19.006 | 33.446 | 44.035 | readback p50 4.739 / CPU copy p50 2.346 / upload p50 1.622 | 0.0 |
| NVIDIA GeForce RTX 4080 → Intel(R) UHD Graphics 770 | 2560x1440 | bgra | shared NT handle | failed: open on Intel(R) UHD Graphics 770: HRESULT 0x80070057 |||||| 
| NVIDIA GeForce RTX 4080 → Intel(R) UHD Graphics 770 | 2560x1440 | bgra | shared legacy handle | failed: open on Intel(R) UHD Graphics 770: HRESULT 0x80070057 |||||| 
| NVIDIA GeForce RTX 4080 → Intel(R) UHD Graphics 770 | 2560x1440 | bgra | staging readback + upload | 10.852 | 17.828 | 24.320 | 64.186 | readback p50 4.716 / CPU copy p50 3.586 / upload p50 1.568 | 0.0 |
| NVIDIA GeForce RTX 4080 (same GPU) | 2560x1440 | nv12 | same-GPU CopyResource | 0.113 | 0.359 | 0.901 | 3.887 |  | 0.0 |
| NVIDIA GeForce RTX 4080 → AMD Radeon RX 6700 XT | 2560x1440 | nv12 | shared NT handle | failed: open on AMD Radeon RX 6700 XT: HRESULT 0x80070057 |||||| 
| NVIDIA GeForce RTX 4080 → AMD Radeon RX 6700 XT | 2560x1440 | nv12 | shared legacy handle | failed: open on AMD Radeon RX 6700 XT: HRESULT 0x80070057 |||||| 
| NVIDIA GeForce RTX 4080 → AMD Radeon RX 6700 XT | 2560x1440 | nv12 | staging readback + upload | 3.446 | 4.195 | 6.158 | 13.335 | readback p50 2.083 / CPU copy p50 0.811 / upload p50 0.533 | 0.0 |
| NVIDIA GeForce RTX 4080 → Intel(R) UHD Graphics 770 | 2560x1440 | nv12 | shared NT handle | failed: open on Intel(R) UHD Graphics 770: HRESULT 0x80070057 |||||| 
| NVIDIA GeForce RTX 4080 → Intel(R) UHD Graphics 770 | 2560x1440 | nv12 | shared legacy handle | failed: open on Intel(R) UHD Graphics 770: HRESULT 0x80070057 |||||| 
| NVIDIA GeForce RTX 4080 → Intel(R) UHD Graphics 770 | 2560x1440 | nv12 | staging readback + upload | 6.672 | 11.983 | 23.666 | 178.327 | readback p50 2.124 / CPU copy p50 1.783 / upload p50 2.541 | 0.0 |
| NVIDIA GeForce RTX 4080 (same GPU) | 2560x1440 | p010 | same-GPU CopyResource | 0.113 | 0.164 | 1.313 | 3.905 |  | 0.0 |
| NVIDIA GeForce RTX 4080 → AMD Radeon RX 6700 XT | 2560x1440 | p010 | shared NT handle | failed: open on AMD Radeon RX 6700 XT: HRESULT 0x80070057 |||||| 
| NVIDIA GeForce RTX 4080 → AMD Radeon RX 6700 XT | 2560x1440 | p010 | shared legacy handle | failed: open on AMD Radeon RX 6700 XT: HRESULT 0x80070057 |||||| 
| NVIDIA GeForce RTX 4080 → AMD Radeon RX 6700 XT | 2560x1440 | p010 | staging readback + upload | 6.138 | 10.833 | 22.747 | 46.015 | readback p50 3.691 / CPU copy p50 1.655 / upload p50 0.746 | 0.0 |
| NVIDIA GeForce RTX 4080 → Intel(R) UHD Graphics 770 | 2560x1440 | p010 | shared NT handle | failed: open on Intel(R) UHD Graphics 770: HRESULT 0x80070057 |||||| 
| NVIDIA GeForce RTX 4080 → Intel(R) UHD Graphics 770 | 2560x1440 | p010 | shared legacy handle | failed: open on Intel(R) UHD Graphics 770: HRESULT 0x80070057 |||||| 
| NVIDIA GeForce RTX 4080 → Intel(R) UHD Graphics 770 | 2560x1440 | p010 | staging readback + upload | 9.074 | 15.628 | 22.449 | 97.637 | readback p50 3.730 / CPU copy p50 2.376 / upload p50 2.619 | 0.0 |
