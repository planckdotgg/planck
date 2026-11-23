NVENC on NVIDIA GeForce RTX 4080: API 13.1, codecs=[H.264 HEVC AV1] chroma=map[H.264:[NV12 YUV444] HEVC:[NV12 P010 YUV444 YUV444P10] AV1:[NV12 P010]] engines=2 subframe=true err=<nil>

AMF runtime 1.4.37.0 (err=<nil>)
  AMF on AMD Radeon RX 6700 XT H.264: accel=0 maxBitrate=0 maxProfile=0 streams=0 hwInstances=0 maxThroughputMB=0 sliceOutput=false width=[128 4096] height=[128 4096] inputFormats=[7 2 3 5 4 1]
  AMF on AMD Radeon RX 6700 XT HEVC: accel=0 maxBitrate=1000000000 maxProfile=2 streams=16 hwInstances=1 maxThroughputMB=2842639 sliceOutput=true width=[128 8192] height=[128 4352] inputFormats=[7 2 3 5 4 1 10]
  AMF on AMD Radeon RX 6700 XT AV1: amf: CreateComponent AMFVideoEncoderHW_AV1: AMF_RESULT 30
  AMF on NVIDIA GeForce RTX 4080: amf: InitDX11: AMF_RESULT 4
  AMF on Intel(R) UHD Graphics 770: amf: InitDX11: AMF_RESULT 4

oneVPL dispatcher (libvpl.dll): 1 implementations (err=<nil>)
  "mfx-gen" impl=2 accel=0x300 api=2.8 vendor=0x8086 device="a780/2" session ok, API 2.8, impl 0x306
    encoder "VP9 " maxLevel=0 profiles: 1(8192x8192 [NV12 P010 AYUV Y410]) 2(8192x8192 [NV12 P010 AYUV Y410]) 3(8192x8192 [NV12 P010 AYUV Y410]) 4(8192x8192 [NV12 P010 AYUV Y410])
    encoder "AVC " maxLevel=52 profiles: 66(4096x4096 [NV12 RGB4 BGR4 YUY2 AYUV]) 578(4096x4096 [NV12 RGB4 BGR4 YUY2 AYUV]) 77(4096x4096 [NV12 RGB4 BGR4 YUY2 AYUV]) 100(4096x4096 [NV12 RGB4 BGR4 YUY2 AYUV]) 12388(4096x4096 [NV12 RGB4 BGR4 YUY2 AYUV]) 4196(4096x4096 [NV12 RGB4 BGR4 YUY2 AYUV])
    encoder "MPG2" maxLevel=4 profiles: 80(1920x1920 [NV12]) 64(1920x1920 [NV12]) 16(1920x1920 [NV12])
    encoder "HEVC" maxLevel=318 profiles: 1(16384x12288 [P010 P210 Y210 Y410 RG10 NV12 YUY2 RGB4 BGR4 P016 Y216 Y416 AYUV]) 2(16384x12288 [P010 P210 Y210 Y410 RG10 NV12 YUY2 RGB4 BGR4 P016 Y216 Y416 AYUV]) 3(16384x12288 [P010 P210 Y210 Y410 RG10 NV12 YUY2 RGB4 BGR4 P016 Y216 Y416 AYUV]) 4(16384x12288 [P010 P210 Y210 Y410 RG10 NV12 YUY2 RGB4 BGR4 P016 Y216 Y416 AYUV]) 9(16384x12288 [P010 P210 Y210 Y410 RG10 NV12 YUY2 RGB4 BGR4 P016 Y216 Y416 AYUV])
    encoder "JPEG" maxLevel=0 profiles: 1(16384x16384 [NV12 YV12 YUY2 RGB4])
