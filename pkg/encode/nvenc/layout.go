// Package nvenc drives NVIDIA's hardware encoder through nvEncodeAPI64.dll's
// function table, with no cgo and no NVIDIA SDK at build time.
package nvenc

// Struct sizes, offsets, and versions of NVENC API 13.1 (nvEncodeAPI.h from
// FFmpeg's nv-codec-headers). Each struct is a zeroed byte buffer written at
// these offsets, which keeps the 3-5 KiB parameter blocks out of Go types.

const (
	apiMajor   = 13
	apiMinor   = 1
	apiVersion = apiMajor | apiMinor<<24
)

func structVersion(v uint32) uint32 { return apiVersion | v<<16 | 0x7<<28 }

var (
	verFunctionList    = structVersion(2)
	verOpenSession     = structVersion(1)
	verCapsParam       = structVersion(1)
	verPresetConfig    = structVersion(5) | 1<<31
	verConfig          = structVersion(9) | 1<<31
	verRCParams        = structVersion(1)
	verInitialize      = structVersion(7) | 1<<31
	verRegister        = structVersion(5)
	verMapInput        = structVersion(4)
	verEventParams     = structVersion(2)
	verCreateBitstream = structVersion(1)
	verPicParams       = structVersion(7) | 1<<31
	verLockBitstream   = structVersion(2) | 1<<31
	verReconfigure     = structVersion(2) | 1<<31
)

// Function table offsets in NV_ENCODE_API_FUNCTION_LIST (2552 bytes).
const (
	sizeFunctionList          = 2552
	fnGetEncodeGUIDCount      = 16
	fnGetEncodeProfileGUIDs   = 32
	fnGetEncodeGUIDs          = 40
	fnGetInputFormatCount     = 48
	fnGetInputFormats         = 56
	fnGetEncodeCaps           = 64
	fnInitializeEncoder       = 96
	fnCreateBitstreamBuffer   = 120
	fnDestroyBitstreamBuffer  = 128
	fnEncodePicture           = 136
	fnLockBitstream           = 144
	fnUnlockBitstream         = 152
	fnRegisterAsyncEvent      = 192
	fnUnregisterAsyncEvent    = 200
	fnMapInputResource        = 208
	fnUnmapInputResource      = 216
	fnDestroyEncoder          = 224
	fnInvalidateRefFrames     = 232
	fnOpenEncodeSessionEx     = 240
	fnRegisterResource        = 248
	fnUnregisterResource      = 256
	fnReconfigureEncoder      = 264
	fnGetLastErrorString      = 304
	fnGetEncodePresetConfigEx = 320
)

const (
	sizeOpenSession = 1552
	osDeviceType    = 4
	osDevice        = 8
	osAPIVersion    = 24

	sizeCapsParam = 256
	capsToQuery   = 4

	sizePresetConfig = 5128
	pcPresetCfg      = 8

	sizeConfig        = 3584
	cfgProfileGUID    = 4
	cfgGopLength      = 20
	cfgFrameIntervalP = 24
	cfgRC             = 40
	cfgCodec          = 168

	rcVersion    = 0
	rcMode       = 4
	rcAvgBitrate = 20
	rcMaxBitrate = 24
	rcVBVSize    = 28
	rcVBVInit    = 32
	rcMultiPass  = 100

	hevcBits               = 16
	hevcIdrPeriod          = 20
	hevcIntraRefreshPeriod = 24
	hevcIntraRefreshCnt    = 28
	hevcMaxRefDPB          = 32
	hevcSliceMode          = 52
	hevcSliceModeData      = 56
	hevcVUI                = 64
	hevcOutputBitDepth     = 200
	hevcInputBitDepth      = 204

	hevcBitRepeatSPSPPS = 7
	hevcBitIntraRefresh = 8
	hevcBitChromaFormat = 9 // 2 bits

	av1Bits               = 16
	av1IdrPeriod          = 20
	av1IntraRefreshPeriod = 24
	av1IntraRefreshCnt    = 28
	av1MaxRefDPB          = 32
	av1NumTileColumns     = 36
	av1NumTileRows        = 40
	av1ColorPrimaries     = 68
	av1Transfer           = 72
	av1Matrix             = 76
	av1ColorRange         = 80
	av1OutputBitDepth     = 112
	av1InputBitDepth      = 116

	av1BitRepeatSeqHdr = 5
	av1BitIntraRefresh = 6
	av1BitChromaFormat = 7 // 2 bits

	sizeInitialize   = 1800
	initEncodeGUID   = 4
	initPresetGUID   = 20
	initWidth        = 36
	initHeight       = 40
	initDarWidth     = 44
	initDarHeight    = 48
	initFrameRateNum = 52
	initFrameRateDen = 56
	initEncodeAsync  = 60
	initPTD          = 64
	initBits         = 68
	initEncodeConfig = 88
	initMaxWidth     = 96
	initMaxHeight    = 100
	initTuningInfo   = 136
	initBufferFormat = 140

	initBitReportSliceOffsets = 0
	initBitSubFrameWrite      = 1
	initBitSplitEncode        = 5 // 4 bits

	sizeRegister    = 1536
	regResourceType = 4
	regWidth        = 8
	regHeight       = 12
	regPitch        = 16
	regResource     = 24
	regRegistered   = 32
	regBufferFormat = 40
	regBufferUsage  = 44

	sizeMapInput    = 1544
	mapRegistered   = 16
	mapMapped       = 24
	mapMappedFormat = 32

	sizeEventParams = 1544
	evEvent         = 8

	sizeCreateBitstream = 776
	cbBitstream         = 16

	sizePicParams  = 3360
	picInputWidth  = 4
	picInputHeight = 8
	picInputPitch  = 12
	picFlags       = 16
	picFrameIdx    = 20
	picTimeStamp   = 24
	picInputBuffer = 40
	picOutput      = 48
	picEvent       = 56
	picBufferFmt   = 64
	picStruct      = 68

	sizeLockBitstream = 1544
	lockBits          = 4
	lockOutput        = 8
	lockSliceOffsets  = 16
	lockFrameIdx      = 24
	lockHWStatus      = 28
	lockNumSlices     = 32
	lockSize          = 36
	lockTimeStamp     = 40
	lockPtr           = 56
	lockPictureType   = 64

	lockBitDoNotWait = 0

	sizeReconfigure = 1816
	reconfInit      = 8
)

const (
	deviceTypeDirectX = 0
	resourceDirectX   = 0
	usageInputImage   = 0

	rcCBR = 2

	tuningUltraLowLatency = 3

	picStructFrame = 1

	picFlagForceIDR     = 2
	picFlagOutputSPSPPS = 4

	picTypeIDR = 3
	picTypeI   = 2

	bitDepth8  = 8
	bitDepth10 = 10

	infiniteGOP = 0xffffffff

	statusSuccess     = 0
	statusLockBusy    = 13
	statusEncoderBusy = 18
)

// Buffer formats.
const (
	bufNV12     = 0x1
	bufYUV444   = 0x1000
	bufYUV42010 = 0x10000
	bufYUV44410 = 0x100000
	bufARGB     = 0x1000000
	bufARGB10   = 0x2000000
	bufAYUV     = 0x4000000
	bufABGR     = 0x10000000
	bufABGR10   = 0x20000000
)

// Capability queries (NV_ENC_CAPS).
const (
	capsSupportSubframeReadback   = 23
	capsSupportIntraRefresh       = 25
	capsSupportRefPicInvalidation = 28
	capsWidthMax                  = 16
	capsHeightMax                 = 17
	capsSupportYUV444             = 33
	capsSupport10Bit              = 39
	capsNumEncoderEngines         = 49
	capsSingleSliceIntraRefresh   = 50
	capsSupportDynamicSliceMode   = 27
)
