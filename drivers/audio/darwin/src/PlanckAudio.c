// Planck Virtual Audio for macOS: a Core Audio AudioServerPlugIn with one
// device that has an output stream and an input stream, wired together.
// Whatever plays to "Planck Virtual Audio" is recorded from it. Both
// streams have exactly one format, 48 kHz 32-bit float stereo, so the two
// sides can never disagree; Core Audio converts mono or int16 clients.
//
// Copyright (c) 2026 Planck contributors. Apache License 2.0; see LICENSE
// at the repository root.

#include <CoreAudio/AudioServerPlugIn.h>
#include <mach/mach_time.h>
#include <pthread.h>
#include <stdatomic.h>
#include <stddef.h>
#include <string.h>

#include "PlanckAudio.h"

enum {
    kObjectPlugIn = kAudioObjectPlugInObject,
    kObjectDevice = 2,
    kObjectInput = 3,
    kObjectOutput = 4,
};

#define kDeviceName CFSTR("Planck Virtual Audio")
#define kManufacturer CFSTR("Planck")
#define kDeviceUID CFSTR(PLANCK_AUDIO_DEVICE_UID)
#define kModelUID CFSTR("gg.planck.audio.model")

static const AudioStreamBasicDescription kFormat = {
    .mSampleRate = PLANCK_AUDIO_SAMPLE_RATE,
    .mFormatID = kAudioFormatLinearPCM,
    .mFormatFlags = kAudioFormatFlagsNativeFloatPacked,
    .mBytesPerPacket = PLANCK_AUDIO_CHANNELS * sizeof(Float32),
    .mFramesPerPacket = 1,
    .mBytesPerFrame = PLANCK_AUDIO_CHANNELS * sizeof(Float32),
    .mChannelsPerFrame = PLANCK_AUDIO_CHANNELS,
    .mBitsPerChannel = 32,
};

static pthread_mutex_t gStateMutex = PTHREAD_MUTEX_INITIALIZER;
static UInt32 gIORunning;
static Boolean gInputActive = true;
static Boolean gOutputActive = true;

// Clock: one zero time stamp every PLANCK_AUDIO_RING_FRAMES frames,
// counted on the host clock from the moment IO started.
static pthread_mutex_t gClockMutex = PTHREAD_MUTEX_INITIALIZER;
static Float64 gHostTicksPerFrame;
static UInt64 gAnchorHostTime;
static UInt64 gTimeStamps;

// The loopback ring is indexed by sample time. Only the IO thread touches
// it while IO runs. Frames in [gWriteStart, gWriteEnd) that are still in
// the ring hold what was played; any other frame reads as silence.
static Float32 gRing[PLANCK_AUDIO_RING_FRAMES * PLANCK_AUDIO_CHANNELS];
static Float64 gWriteStart;
static Float64 gWriteEnd;

static _Atomic UInt32 gRefCount = 1;

static AudioServerPlugInDriverInterface gInterface;
static AudioServerPlugInDriverInterface* gInterfacePtr = &gInterface;
static AudioServerPlugInDriverRef gDriverRef = &gInterfacePtr;

// ---- IUnknown ----

static HRESULT QueryInterface(void* inDriver, REFIID inUUID, LPVOID* outInterface) {
    if (inDriver != gDriverRef || outInterface == NULL) {
        return kAudioHardwareBadObjectError;
    }
    CFUUIDRef asked = CFUUIDCreateFromUUIDBytes(NULL, inUUID);
    if (asked == NULL) {
        return kAudioHardwareIllegalOperationError;
    }
    Boolean ok = CFEqual(asked, IUnknownUUID) || CFEqual(asked, kAudioServerPlugInDriverInterfaceUUID);
    CFRelease(asked);
    if (!ok) {
        *outInterface = NULL;
        return E_NOINTERFACE;
    }
    atomic_fetch_add(&gRefCount, 1);
    *outInterface = gDriverRef;
    return S_OK;
}

static ULONG AddRef(void* inDriver) {
    if (inDriver != gDriverRef) {
        return 0;
    }
    return atomic_fetch_add(&gRefCount, 1) + 1;
}

// The driver is a static singleton: the count only reports.
static ULONG Release(void* inDriver) {
    if (inDriver != gDriverRef) {
        return 0;
    }
    UInt32 n = atomic_load(&gRefCount);
    while (n > 0 && !atomic_compare_exchange_weak(&gRefCount, &n, n - 1)) {
    }
    return n > 0 ? n - 1 : 0;
}

// ---- Basic operations ----

static OSStatus Initialize(AudioServerPlugInDriverRef inDriver, AudioServerPlugInHostRef inHost) {
    (void)inHost;
    if (inDriver != gDriverRef) {
        return kAudioHardwareBadObjectError;
    }
    mach_timebase_info_data_t tb;
    mach_timebase_info(&tb);
    gHostTicksPerFrame = (1e9 * (Float64)tb.denom / (Float64)tb.numer) / PLANCK_AUDIO_SAMPLE_RATE;
    return noErr;
}

static OSStatus CreateDevice(AudioServerPlugInDriverRef d, CFDictionaryRef desc, const AudioServerPlugInClientInfo* ci, AudioObjectID* out) {
    (void)d, (void)desc, (void)ci, (void)out;
    return kAudioHardwareUnsupportedOperationError;
}

static OSStatus DestroyDevice(AudioServerPlugInDriverRef d, AudioObjectID dev) {
    (void)d, (void)dev;
    return kAudioHardwareUnsupportedOperationError;
}

static OSStatus DeviceClient(AudioServerPlugInDriverRef d, AudioObjectID dev, const AudioServerPlugInClientInfo* ci) {
    (void)ci;
    if (d != gDriverRef) {
        return kAudioHardwareBadObjectError;
    }
    return dev == kObjectDevice ? noErr : kAudioHardwareBadObjectError;
}

static OSStatus ConfigurationChange(AudioServerPlugInDriverRef d, AudioObjectID dev, UInt64 action, void* info) {
    (void)action, (void)info;
    if (d != gDriverRef) {
        return kAudioHardwareBadObjectError;
    }
    return dev == kObjectDevice ? noErr : kAudioHardwareBadObjectError;
}

// ---- Properties ----

// writeValue stores one value of size n, or only reports its size when out
// is NULL.
static OSStatus writeValue(const void* v, UInt32 n, UInt32 inSize, UInt32* outSize, void* out) {
    if (out != NULL) {
        if (inSize < n) {
            return kAudioHardwareBadPropertySizeError;
        }
        memcpy(out, v, n);
    }
    *outSize = n;
    return noErr;
}

#define VALUE(T, v)                                                    \
    do {                                                               \
        T value_ = (v);                                                \
        return writeValue(&value_, sizeof(T), inSize, outSize, out);   \
    } while (0)

// writeIDs stores as many of ids as fit; a size query reports all of them.
static OSStatus writeIDs(const AudioObjectID* ids, UInt32 count, UInt32 inSize, UInt32* outSize, void* out) {
    if (out != NULL) {
        UInt32 fit = inSize / sizeof(AudioObjectID);
        count = fit < count ? fit : count;
        memcpy(out, ids, count * sizeof(AudioObjectID));
    }
    *outSize = count * sizeof(AudioObjectID);
    return noErr;
}

// deviceStreams are the device's streams in a scope.
static UInt32 deviceStreams(AudioObjectPropertyScope scope, AudioObjectID ids[2]) {
    UInt32 n = 0;
    if (scope == kAudioObjectPropertyScopeGlobal || scope == kAudioObjectPropertyScopeInput) {
        ids[n++] = kObjectInput;
    }
    if (scope == kAudioObjectPropertyScopeGlobal || scope == kAudioObjectPropertyScopeOutput) {
        ids[n++] = kObjectOutput;
    }
    return n;
}

static OSStatus plugInProperty(const AudioObjectPropertyAddress* a, UInt32 qSize, const void* q, UInt32 inSize, UInt32* outSize, void* out) {
    static const AudioObjectID devices[] = {kObjectDevice};
    switch (a->mSelector) {
    case kAudioObjectPropertyBaseClass:
        VALUE(AudioClassID, kAudioObjectClassID);
    case kAudioObjectPropertyClass:
        VALUE(AudioClassID, kAudioPlugInClassID);
    case kAudioObjectPropertyOwner:
        VALUE(AudioObjectID, kAudioObjectUnknown);
    case kAudioObjectPropertyManufacturer:
        VALUE(CFStringRef, kManufacturer);
    case kAudioObjectPropertyOwnedObjects:
    case kAudioPlugInPropertyDeviceList:
        return writeIDs(devices, 1, inSize, outSize, out);
    case kAudioPlugInPropertyTranslateUIDToDevice: {
        if (out != NULL && (qSize != sizeof(CFStringRef) || q == NULL)) {
            return kAudioHardwareBadPropertySizeError;
        }
        AudioObjectID id = kAudioObjectUnknown;
        if (out != NULL && *(const CFStringRef*)q != NULL && CFEqual(*(const CFStringRef*)q, kDeviceUID)) {
            id = kObjectDevice;
        }
        VALUE(AudioObjectID, id);
    }
    case kAudioPlugInPropertyResourceBundle:
        VALUE(CFStringRef, CFSTR(""));
    }
    return kAudioHardwareUnknownPropertyError;
}

static OSStatus deviceProperty(const AudioObjectPropertyAddress* a, UInt32 inSize, UInt32* outSize, void* out) {
    static const AudioObjectID related[] = {kObjectDevice};
    AudioObjectID streams[2];
    switch (a->mSelector) {
    case kAudioObjectPropertyBaseClass:
        VALUE(AudioClassID, kAudioObjectClassID);
    case kAudioObjectPropertyClass:
        VALUE(AudioClassID, kAudioDeviceClassID);
    case kAudioObjectPropertyOwner:
        VALUE(AudioObjectID, kObjectPlugIn);
    case kAudioObjectPropertyName:
        VALUE(CFStringRef, kDeviceName);
    case kAudioObjectPropertyManufacturer:
        VALUE(CFStringRef, kManufacturer);
    case kAudioObjectPropertyOwnedObjects:
    case kAudioDevicePropertyStreams:
        return writeIDs(streams, deviceStreams(a->mScope, streams), inSize, outSize, out);
    case kAudioObjectPropertyControlList:
        return writeIDs(NULL, 0, inSize, outSize, out);
    case kAudioDevicePropertyDeviceUID:
        VALUE(CFStringRef, kDeviceUID);
    case kAudioDevicePropertyModelUID:
        VALUE(CFStringRef, kModelUID);
    case kAudioDevicePropertyTransportType:
        VALUE(UInt32, kAudioDeviceTransportTypeVirtual);
    case kAudioDevicePropertyRelatedDevices:
        return writeIDs(related, 1, inSize, outSize, out);
    case kAudioDevicePropertyClockDomain:
        VALUE(UInt32, 0);
    case kAudioDevicePropertyDeviceIsAlive:
        VALUE(UInt32, 1);
    case kAudioDevicePropertyDeviceIsRunning: {
        pthread_mutex_lock(&gStateMutex);
        UInt32 running = gIORunning > 0;
        pthread_mutex_unlock(&gStateMutex);
        VALUE(UInt32, running);
    }
    case kAudioDevicePropertyDeviceCanBeDefaultDevice:
        // Never the default output: game audio would go into the mic.
        VALUE(UInt32, a->mScope == kAudioObjectPropertyScopeInput);
    case kAudioDevicePropertyDeviceCanBeDefaultSystemDevice:
        VALUE(UInt32, 0);
    case kAudioDevicePropertyLatency:
    case kAudioDevicePropertySafetyOffset:
        VALUE(UInt32, 0);
    case kAudioDevicePropertyNominalSampleRate:
        VALUE(Float64, PLANCK_AUDIO_SAMPLE_RATE);
    case kAudioDevicePropertyAvailableNominalSampleRates: {
        AudioValueRange r = {PLANCK_AUDIO_SAMPLE_RATE, PLANCK_AUDIO_SAMPLE_RATE};
        VALUE(AudioValueRange, r);
    }
    case kAudioDevicePropertyIsHidden:
        VALUE(UInt32, 0);
    case kAudioDevicePropertyZeroTimeStampPeriod:
        VALUE(UInt32, PLANCK_AUDIO_RING_FRAMES);
    case kAudioDevicePropertyPreferredChannelsForStereo: {
        UInt32 lr[2] = {1, 2};
        return writeValue(lr, sizeof(lr), inSize, outSize, out);
    }
    case kAudioDevicePropertyPreferredChannelLayout: {
        AudioChannelLayout l = {.mChannelLayoutTag = kAudioChannelLayoutTag_Stereo};
        VALUE(AudioChannelLayout, l);
    }
    }
    return kAudioHardwareUnknownPropertyError;
}

static OSStatus streamProperty(AudioObjectID obj, const AudioObjectPropertyAddress* a, UInt32 inSize, UInt32* outSize, void* out) {
    Boolean input = obj == kObjectInput;
    switch (a->mSelector) {
    case kAudioObjectPropertyBaseClass:
        VALUE(AudioClassID, kAudioObjectClassID);
    case kAudioObjectPropertyClass:
        VALUE(AudioClassID, kAudioStreamClassID);
    case kAudioObjectPropertyOwner:
        VALUE(AudioObjectID, kObjectDevice);
    case kAudioObjectPropertyOwnedObjects:
        return writeIDs(NULL, 0, inSize, outSize, out);
    case kAudioStreamPropertyIsActive: {
        pthread_mutex_lock(&gStateMutex);
        UInt32 active = input ? gInputActive : gOutputActive;
        pthread_mutex_unlock(&gStateMutex);
        VALUE(UInt32, active);
    }
    case kAudioStreamPropertyDirection:
        VALUE(UInt32, input ? 1 : 0);
    case kAudioStreamPropertyTerminalType:
        VALUE(UInt32, input ? kAudioStreamTerminalTypeMicrophone : kAudioStreamTerminalTypeSpeaker);
    case kAudioStreamPropertyStartingChannel:
        VALUE(UInt32, 1);
    case kAudioStreamPropertyLatency:
        VALUE(UInt32, 0);
    case kAudioStreamPropertyVirtualFormat:
    case kAudioStreamPropertyPhysicalFormat:
        VALUE(AudioStreamBasicDescription, kFormat);
    case kAudioStreamPropertyAvailableVirtualFormats:
    case kAudioStreamPropertyAvailablePhysicalFormats: {
        AudioStreamRangedDescription r = {kFormat, {PLANCK_AUDIO_SAMPLE_RATE, PLANCK_AUDIO_SAMPLE_RATE}};
        VALUE(AudioStreamRangedDescription, r);
    }
    }
    return kAudioHardwareUnknownPropertyError;
}

// property answers HasProperty, GetPropertyDataSize (out NULL) and
// GetPropertyData from one table, so the three cannot disagree.
static OSStatus property(AudioServerPlugInDriverRef d, AudioObjectID obj, const AudioObjectPropertyAddress* a,
                         UInt32 qSize, const void* q, UInt32 inSize, UInt32* outSize, void* out) {
    if (d != gDriverRef) {
        return kAudioHardwareBadObjectError;
    }
    if (a == NULL || outSize == NULL) {
        return kAudioHardwareIllegalOperationError;
    }
    switch (obj) {
    case kObjectPlugIn:
        return plugInProperty(a, qSize, q, inSize, outSize, out);
    case kObjectDevice:
        return deviceProperty(a, inSize, outSize, out);
    case kObjectInput:
    case kObjectOutput:
        return streamProperty(obj, a, inSize, outSize, out);
    }
    return kAudioHardwareBadObjectError;
}

static Boolean HasProperty(AudioServerPlugInDriverRef d, AudioObjectID obj, pid_t pid, const AudioObjectPropertyAddress* a) {
    (void)pid;
    UInt32 size;
    return property(d, obj, a, 0, NULL, 0, &size, NULL) == noErr;
}

static Boolean settable(AudioObjectID obj, AudioObjectPropertySelector sel) {
    switch (obj) {
    case kObjectDevice:
        return sel == kAudioDevicePropertyNominalSampleRate;
    case kObjectInput:
    case kObjectOutput:
        return sel == kAudioStreamPropertyIsActive || sel == kAudioStreamPropertyVirtualFormat || sel == kAudioStreamPropertyPhysicalFormat;
    }
    return false;
}

static OSStatus IsPropertySettable(AudioServerPlugInDriverRef d, AudioObjectID obj, pid_t pid, const AudioObjectPropertyAddress* a, Boolean* outSettable) {
    if (outSettable == NULL) {
        return kAudioHardwareIllegalOperationError;
    }
    if (!HasProperty(d, obj, pid, a)) {
        return kAudioHardwareUnknownPropertyError;
    }
    *outSettable = settable(obj, a->mSelector);
    return noErr;
}

static OSStatus GetPropertyDataSize(AudioServerPlugInDriverRef d, AudioObjectID obj, pid_t pid, const AudioObjectPropertyAddress* a,
                                    UInt32 qSize, const void* q, UInt32* outSize) {
    (void)pid;
    return property(d, obj, a, qSize, q, 0, outSize, NULL);
}

static OSStatus GetPropertyData(AudioServerPlugInDriverRef d, AudioObjectID obj, pid_t pid, const AudioObjectPropertyAddress* a,
                                UInt32 qSize, const void* q, UInt32 inSize, UInt32* outSize, void* out) {
    (void)pid;
    if (out == NULL) {
        return kAudioHardwareIllegalOperationError;
    }
    return property(d, obj, a, qSize, q, inSize, outSize, out);
}

static Boolean isOurFormat(const AudioStreamBasicDescription* f) {
    return f->mSampleRate == kFormat.mSampleRate && f->mFormatID == kFormat.mFormatID && f->mFormatFlags == kFormat.mFormatFlags &&
           f->mBytesPerFrame == kFormat.mBytesPerFrame && f->mChannelsPerFrame == kFormat.mChannelsPerFrame &&
           f->mBitsPerChannel == kFormat.mBitsPerChannel;
}

static OSStatus SetPropertyData(AudioServerPlugInDriverRef d, AudioObjectID obj, pid_t pid, const AudioObjectPropertyAddress* a,
                                UInt32 qSize, const void* q, UInt32 inSize, const void* in) {
    (void)pid, (void)qSize, (void)q;
    if (d != gDriverRef) {
        return kAudioHardwareBadObjectError;
    }
    if (a == NULL || in == NULL) {
        return kAudioHardwareIllegalOperationError;
    }
    if (!settable(obj, a->mSelector)) {
        return HasProperty(d, obj, pid, a) ? kAudioHardwareUnsupportedOperationError : kAudioHardwareUnknownPropertyError;
    }
    switch (a->mSelector) {
    case kAudioDevicePropertyNominalSampleRate:
        if (inSize != sizeof(Float64)) {
            return kAudioHardwareBadPropertySizeError;
        }
        return *(const Float64*)in == PLANCK_AUDIO_SAMPLE_RATE ? noErr : kAudioDeviceUnsupportedFormatError;
    case kAudioStreamPropertyVirtualFormat:
    case kAudioStreamPropertyPhysicalFormat:
        if (inSize != sizeof(AudioStreamBasicDescription)) {
            return kAudioHardwareBadPropertySizeError;
        }
        return isOurFormat(in) ? noErr : kAudioDeviceUnsupportedFormatError;
    case kAudioStreamPropertyIsActive:
        if (inSize != sizeof(UInt32)) {
            return kAudioHardwareBadPropertySizeError;
        }
        pthread_mutex_lock(&gStateMutex);
        *(obj == kObjectInput ? &gInputActive : &gOutputActive) = *(const UInt32*)in != 0;
        pthread_mutex_unlock(&gStateMutex);
        return noErr;
    }
    return kAudioHardwareUnknownPropertyError;
}

// ---- IO ----

static OSStatus StartIO(AudioServerPlugInDriverRef d, AudioObjectID dev, UInt32 client) {
    (void)client;
    if (d != gDriverRef || dev != kObjectDevice) {
        return kAudioHardwareBadObjectError;
    }
    pthread_mutex_lock(&gStateMutex);
    if (gIORunning == UINT32_MAX) {
        pthread_mutex_unlock(&gStateMutex);
        return kAudioHardwareIllegalOperationError;
    }
    if (gIORunning++ == 0) {
        memset(gRing, 0, sizeof(gRing));
        gWriteStart = gWriteEnd = 0;
        pthread_mutex_lock(&gClockMutex);
        gTimeStamps = 0;
        gAnchorHostTime = mach_absolute_time();
        pthread_mutex_unlock(&gClockMutex);
    }
    pthread_mutex_unlock(&gStateMutex);
    return noErr;
}

static OSStatus StopIO(AudioServerPlugInDriverRef d, AudioObjectID dev, UInt32 client) {
    (void)client;
    if (d != gDriverRef || dev != kObjectDevice) {
        return kAudioHardwareBadObjectError;
    }
    pthread_mutex_lock(&gStateMutex);
    OSStatus err = gIORunning > 0 ? noErr : kAudioHardwareIllegalOperationError;
    if (gIORunning > 0) {
        gIORunning--;
    }
    pthread_mutex_unlock(&gStateMutex);
    return err;
}

static OSStatus GetZeroTimeStamp(AudioServerPlugInDriverRef d, AudioObjectID dev, UInt32 client, Float64* outSampleTime, UInt64* outHostTime, UInt64* outSeed) {
    (void)client;
    if (d != gDriverRef || dev != kObjectDevice) {
        return kAudioHardwareBadObjectError;
    }
    Float64 ticksPerPeriod = gHostTicksPerFrame * PLANCK_AUDIO_RING_FRAMES;
    pthread_mutex_lock(&gClockMutex);
    UInt64 now = mach_absolute_time();
    if (gAnchorHostTime + (UInt64)((Float64)(gTimeStamps + 1) * ticksPerPeriod) <= now) {
        gTimeStamps++;
    }
    *outSampleTime = (Float64)gTimeStamps * PLANCK_AUDIO_RING_FRAMES;
    *outHostTime = gAnchorHostTime + (UInt64)((Float64)gTimeStamps * ticksPerPeriod);
    pthread_mutex_unlock(&gClockMutex);
    *outSeed = 1;
    return noErr;
}

static OSStatus WillDoIOOperation(AudioServerPlugInDriverRef d, AudioObjectID dev, UInt32 client, UInt32 op, Boolean* outWillDo, Boolean* outInPlace) {
    (void)client;
    if (d != gDriverRef || dev != kObjectDevice) {
        return kAudioHardwareBadObjectError;
    }
    *outWillDo = op == kAudioServerPlugInIOOperationReadInput || op == kAudioServerPlugInIOOperationWriteMix;
    *outInPlace = true;
    return noErr;
}

static OSStatus IOPhase(AudioServerPlugInDriverRef d, AudioObjectID dev, UInt32 client, UInt32 op, UInt32 frames, const AudioServerPlugInIOCycleInfo* info) {
    (void)client, (void)op, (void)frames, (void)info;
    if (d != gDriverRef || dev != kObjectDevice) {
        return kAudioHardwareBadObjectError;
    }
    return noErr;
}

static UInt32 ringIndex(Float64 sampleTime) {
    return (UInt32)((UInt64)sampleTime % PLANCK_AUDIO_RING_FRAMES);
}

void PlanckAudio_WriteRing(const Float32* in, UInt32 frames, Float64 sampleTime) {
    if (sampleTime != gWriteEnd) {
        gWriteStart = sampleTime;
    }
    for (UInt32 i = 0; i < frames; i++) {
        memcpy(&gRing[ringIndex(sampleTime + i) * PLANCK_AUDIO_CHANNELS], &in[i * PLANCK_AUDIO_CHANNELS], kFormat.mBytesPerFrame);
    }
    gWriteEnd = sampleTime + frames;
}

void PlanckAudio_ReadRing(Float32* out, UInt32 frames, Float64 sampleTime) {
    Float64 from = gWriteEnd - PLANCK_AUDIO_RING_FRAMES;
    if (from < gWriteStart) {
        from = gWriteStart;
    }
    for (UInt32 i = 0; i < frames; i++) {
        Float64 t = sampleTime + i;
        Float32* dst = &out[i * PLANCK_AUDIO_CHANNELS];
        if (t >= from && t < gWriteEnd) {
            memcpy(dst, &gRing[ringIndex(t) * PLANCK_AUDIO_CHANNELS], kFormat.mBytesPerFrame);
        } else {
            memset(dst, 0, kFormat.mBytesPerFrame);
        }
    }
}

static OSStatus DoIOOperation(AudioServerPlugInDriverRef d, AudioObjectID dev, AudioObjectID stream, UInt32 client, UInt32 op,
                              UInt32 frames, const AudioServerPlugInIOCycleInfo* info, void* main, void* secondary) {
    (void)client, (void)secondary;
    if (d != gDriverRef || dev != kObjectDevice) {
        return kAudioHardwareBadObjectError;
    }
    if (main == NULL || info == NULL) {
        return noErr;
    }
    if (op == kAudioServerPlugInIOOperationWriteMix && stream == kObjectOutput) {
        PlanckAudio_WriteRing(main, frames, info->mOutputTime.mSampleTime);
    } else if (op == kAudioServerPlugInIOOperationReadInput && stream == kObjectInput) {
        PlanckAudio_ReadRing(main, frames, info->mInputTime.mSampleTime);
    }
    return noErr;
}

static AudioServerPlugInDriverInterface gInterface = {
    NULL,
    QueryInterface,
    AddRef,
    Release,
    Initialize,
    CreateDevice,
    DestroyDevice,
    DeviceClient,
    DeviceClient,
    ConfigurationChange,
    ConfigurationChange,
    HasProperty,
    IsPropertySettable,
    GetPropertyDataSize,
    GetPropertyData,
    SetPropertyData,
    StartIO,
    StopIO,
    GetZeroTimeStamp,
    WillDoIOOperation,
    IOPhase,
    DoIOOperation,
    IOPhase,
};

void* PlanckAudio_Create(CFAllocatorRef allocator, CFUUIDRef requestedType) {
    (void)allocator;
    if (!CFEqual(requestedType, kAudioServerPlugInTypeUUID)) {
        return NULL;
    }
    return gDriverRef;
}
