// Copyright (c) 2026 Planck contributors. Apache License 2.0; see LICENSE
// at the repository root.
//
// Drives the plug-in through its CFPlugIn factory the way coreaudiod does,
// without installing it: properties, the locked format, and loopback IO.

#include <CoreAudio/AudioServerPlugIn.h>
#include <stdio.h>
#include <string.h>

#include "PlanckAudio.h"

static int failures;

#define CHECK(cond)                                                      \
    do {                                                                 \
        if (!(cond)) {                                                   \
            fprintf(stderr, "%s:%d: FAIL %s\n", __FILE__, __LINE__, #cond); \
            failures++;                                                  \
        }                                                                \
    } while (0)

static AudioServerPlugInDriverRef drv;

static OSStatus get(AudioObjectID obj, AudioObjectPropertySelector sel, AudioObjectPropertyScope scope, UInt32 size, void* out, UInt32* used) {
    AudioObjectPropertyAddress a = {sel, scope, kAudioObjectPropertyElementMain};
    UInt32 want = 0;
    OSStatus err = (*drv)->GetPropertyDataSize(drv, obj, 0, &a, 0, NULL, &want);
    if (err != noErr) {
        return err;
    }
    if (!(*drv)->HasProperty(drv, obj, 0, &a)) {
        return kAudioHardwareUnknownPropertyError;
    }
    err = (*drv)->GetPropertyData(drv, obj, 0, &a, 0, NULL, size, used, out);
    if (err == noErr && *used != want && size >= want) {
        fprintf(stderr, "selector %.4s: size %u but data %u\n", (char*)&sel, want, *used);
        failures++;
    }
    return err;
}

static UInt32 getU32(AudioObjectID obj, AudioObjectPropertySelector sel, AudioObjectPropertyScope scope) {
    UInt32 v = 0xFFFFFFFF, used = 0;
    CHECK(get(obj, sel, scope, sizeof(v), &v, &used) == noErr);
    return v;
}

static OSStatus set(AudioObjectID obj, AudioObjectPropertySelector sel, UInt32 size, const void* in) {
    AudioObjectPropertyAddress a = {sel, kAudioObjectPropertyScopeGlobal, kAudioObjectPropertyElementMain};
    return (*drv)->SetPropertyData(drv, obj, 0, &a, 0, NULL, size, in);
}

static void testProperties(void) {
    UInt32 used;
    AudioObjectID devs[4];
    CHECK(get(kAudioObjectPlugInObject, kAudioPlugInPropertyDeviceList, kAudioObjectPropertyScopeGlobal, sizeof(devs), devs, &used) == noErr);
    CHECK(used == sizeof(AudioObjectID));
    AudioObjectID dev = devs[0];

    CFStringRef s = NULL;
    CHECK(get(dev, kAudioObjectPropertyName, kAudioObjectPropertyScopeGlobal, sizeof(s), &s, &used) == noErr);
    CHECK(s != NULL && CFEqual(s, CFSTR("Planck Virtual Audio")));

    CFStringRef uid = CFSTR(PLANCK_AUDIO_DEVICE_UID);
    AudioObjectPropertyAddress tr = {kAudioPlugInPropertyTranslateUIDToDevice, kAudioObjectPropertyScopeGlobal, kAudioObjectPropertyElementMain};
    AudioObjectID found = 0;
    CHECK((*drv)->GetPropertyData(drv, kAudioObjectPlugInObject, 0, &tr, sizeof(uid), &uid, sizeof(found), &used, &found) == noErr);
    CHECK(found == dev);
    CFStringRef other = CFSTR("someone.else");
    CHECK((*drv)->GetPropertyData(drv, kAudioObjectPlugInObject, 0, &tr, sizeof(other), &other, sizeof(found), &used, &found) == noErr);
    CHECK(found == kAudioObjectUnknown);

    AudioObjectID in[2], out[2], all[4];
    CHECK(get(dev, kAudioDevicePropertyStreams, kAudioObjectPropertyScopeInput, sizeof(in), in, &used) == noErr && used == sizeof(AudioObjectID));
    CHECK(get(dev, kAudioDevicePropertyStreams, kAudioObjectPropertyScopeOutput, sizeof(out), out, &used) == noErr && used == sizeof(AudioObjectID));
    CHECK(get(dev, kAudioObjectPropertyOwnedObjects, kAudioObjectPropertyScopeGlobal, sizeof(all), all, &used) == noErr && used == 2 * sizeof(AudioObjectID));
    CHECK(getU32(in[0], kAudioStreamPropertyDirection, kAudioObjectPropertyScopeGlobal) == 1);
    CHECK(getU32(out[0], kAudioStreamPropertyDirection, kAudioObjectPropertyScopeGlobal) == 0);

    CHECK(getU32(dev, kAudioDevicePropertyDeviceCanBeDefaultDevice, kAudioObjectPropertyScopeInput) == 1);
    CHECK(getU32(dev, kAudioDevicePropertyDeviceCanBeDefaultDevice, kAudioObjectPropertyScopeOutput) == 0);
    CHECK(getU32(dev, kAudioDevicePropertyTransportType, kAudioObjectPropertyScopeGlobal) == kAudioDeviceTransportTypeVirtual);

    Float64 rate = 0;
    CHECK(get(dev, kAudioDevicePropertyNominalSampleRate, kAudioObjectPropertyScopeGlobal, sizeof(rate), &rate, &used) == noErr && rate == 48000.0);
    AudioValueRange rates[4];
    CHECK(get(dev, kAudioDevicePropertyAvailableNominalSampleRates, kAudioObjectPropertyScopeGlobal, sizeof(rates), rates, &used) == noErr);
    CHECK(used == sizeof(AudioValueRange) && rates[0].mMinimum == 48000.0 && rates[0].mMaximum == 48000.0);

    AudioStreamBasicDescription fmt[2];
    for (int i = 0; i < 2; i++) {
        AudioObjectID st = i == 0 ? in[0] : out[0];
        CHECK(get(st, kAudioStreamPropertyVirtualFormat, kAudioObjectPropertyScopeGlobal, sizeof(fmt[i]), &fmt[i], &used) == noErr);
        CHECK(fmt[i].mSampleRate == 48000.0 && fmt[i].mChannelsPerFrame == 2 && fmt[i].mBitsPerChannel == 32);
        CHECK(fmt[i].mFormatFlags & kAudioFormatFlagIsFloat);
        AudioStreamRangedDescription ranged[4];
        CHECK(get(st, kAudioStreamPropertyAvailablePhysicalFormats, kAudioObjectPropertyScopeGlobal, sizeof(ranged), ranged, &used) == noErr);
        CHECK(used == sizeof(AudioStreamRangedDescription));
    }
    CHECK(memcmp(&fmt[0], &fmt[1], sizeof(fmt[0])) == 0);

    Float64 r441 = 44100.0;
    CHECK(set(dev, kAudioDevicePropertyNominalSampleRate, sizeof(r441), &r441) == kAudioDeviceUnsupportedFormatError);
    CHECK(set(dev, kAudioDevicePropertyNominalSampleRate, sizeof(rate), &rate) == noErr);
    AudioStreamBasicDescription mono = fmt[0];
    mono.mChannelsPerFrame = 1;
    mono.mBytesPerFrame = mono.mBytesPerPacket = 4;
    CHECK(set(in[0], kAudioStreamPropertyPhysicalFormat, sizeof(mono), &mono) == kAudioDeviceUnsupportedFormatError);
    CHECK(set(in[0], kAudioStreamPropertyPhysicalFormat, sizeof(fmt[0]), &fmt[0]) == noErr);
    CFStringRef name = CFSTR("x");
    CHECK(set(dev, kAudioObjectPropertyName, sizeof(name), &name) == kAudioHardwareUnsupportedOperationError);

    AudioObjectPropertyAddress bogus = {'zzzz', kAudioObjectPropertyScopeGlobal, kAudioObjectPropertyElementMain};
    CHECK(!(*drv)->HasProperty(drv, dev, 0, &bogus));
    CHECK(!(*drv)->HasProperty(drv, 99, 0, &bogus));
}

enum { kFrames = 512 };

static void io(UInt32 op, AudioObjectID stream, Float64 sampleTime, Float32* buf) {
    AudioServerPlugInIOCycleInfo info;
    memset(&info, 0, sizeof(info));
    info.mInputTime.mSampleTime = sampleTime;
    info.mOutputTime.mSampleTime = sampleTime;
    CHECK((*drv)->DoIOOperation(drv, 2, stream, 1, op, kFrames, &info, buf, NULL) == noErr);
}

static int silent(const Float32* b) {
    for (int i = 0; i < kFrames * 2; i++) {
        if (b[i] != 0) {
            return 0;
        }
    }
    return 1;
}

static void testLoopback(void) {
    const AudioObjectID dev = 2, input = 3, output = 4;
    Boolean will, inPlace;
    CHECK((*drv)->WillDoIOOperation(drv, dev, 1, kAudioServerPlugInIOOperationWriteMix, &will, &inPlace) == noErr && will && inPlace);
    CHECK((*drv)->WillDoIOOperation(drv, dev, 1, kAudioServerPlugInIOOperationReadInput, &will, &inPlace) == noErr && will && inPlace);
    CHECK((*drv)->WillDoIOOperation(drv, dev, 1, kAudioServerPlugInIOOperationMixOutput, &will, &inPlace) == noErr && !will);

    CHECK((*drv)->StopIO(drv, dev, 1) == kAudioHardwareIllegalOperationError);
    CHECK((*drv)->StartIO(drv, dev, 1) == noErr);
    CHECK(getU32(dev, kAudioDevicePropertyDeviceIsRunning, kAudioObjectPropertyScopeGlobal) == 1);

    Float64 st0, st1;
    UInt64 ht0, ht1, seed;
    CHECK((*drv)->GetZeroTimeStamp(drv, dev, 1, &st0, &ht0, &seed) == noErr);
    CHECK((*drv)->GetZeroTimeStamp(drv, dev, 1, &st1, &ht1, &seed) == noErr);
    CHECK(st1 >= st0 && ht1 >= ht0 && seed == 1);

    static Float32 played[kFrames * 2], heard[kFrames * 2];
    for (int i = 0; i < kFrames * 2; i++) {
        played[i] = (Float32)(i + 1) / (kFrames * 2);
    }
    io(kAudioServerPlugInIOOperationReadInput, input, 1000, heard);
    CHECK(silent(heard));

    io(kAudioServerPlugInIOOperationWriteMix, output, 1000, played);
    io(kAudioServerPlugInIOOperationReadInput, input, 1000, heard);
    CHECK(memcmp(played, heard, sizeof(played)) == 0);

    // Half of this read was never played: it must be silence, not old data.
    io(kAudioServerPlugInIOOperationReadInput, input, 1000 + kFrames / 2, heard);
    CHECK(memcmp(heard, played + kFrames, sizeof(played) / 2) == 0);
    CHECK(heard[kFrames] == 0 && heard[2 * kFrames - 1] == 0);

    // A new run of output further on: frames from before it are gone, even
    // where the ring slot still holds them.
    io(kAudioServerPlugInIOOperationWriteMix, output, 1000 + PLANCK_AUDIO_RING_FRAMES + 4 * kFrames, played);
    io(kAudioServerPlugInIOOperationReadInput, input, 1000, heard);
    CHECK(silent(heard));
    io(kAudioServerPlugInIOOperationReadInput, input, 1000 + PLANCK_AUDIO_RING_FRAMES + 4 * kFrames, heard);
    CHECK(memcmp(played, heard, sizeof(played)) == 0);

    // The ring keeps one period: audio overwritten by a later lap is gone.
    for (Float64 t = 0; t <= PLANCK_AUDIO_RING_FRAMES; t += kFrames) {
        io(kAudioServerPlugInIOOperationWriteMix, output, 200000 + t, played);
    }
    io(kAudioServerPlugInIOOperationReadInput, input, 200000, heard);
    CHECK(silent(heard));

    CHECK((*drv)->StopIO(drv, dev, 1) == noErr);
    CHECK(getU32(dev, kAudioDevicePropertyDeviceIsRunning, kAudioObjectPropertyScopeGlobal) == 0);

    // Restarting IO starts a new time line with an empty ring.
    CHECK((*drv)->StartIO(drv, dev, 1) == noErr);
    io(kAudioServerPlugInIOOperationReadInput, input, 1000, heard);
    CHECK(silent(heard));
    CHECK((*drv)->StopIO(drv, dev, 1) == noErr);
}

int main(void) {
    CHECK(PlanckAudio_Create(NULL, kAudioServerPlugInDriverInterfaceUUID) == NULL);
    void* raw = PlanckAudio_Create(NULL, kAudioServerPlugInTypeUUID);
    CHECK(raw != NULL);
    if (raw == NULL) {
        return 1;
    }
    AudioServerPlugInDriverRef d = raw;
    CFUUIDBytes iid = CFUUIDGetUUIDBytes(kAudioServerPlugInDriverInterfaceUUID);
    void* iface = NULL;
    CHECK((*d)->QueryInterface(d, iid, &iface) == S_OK && iface == raw);
    CFUUIDBytes other = CFUUIDGetUUIDBytes(kAudioServerPlugInTypeUUID);
    void* none = raw;
    CHECK((*d)->QueryInterface(d, other, &none) == E_NOINTERFACE && none == NULL);
    drv = iface;
    static AudioServerPlugInHostInterface host;
    CHECK((*drv)->Initialize(drv, &host) == noErr);

    testProperties();
    testLoopback();
    (*drv)->Release(drv);

    if (failures) {
        fprintf(stderr, "%d failure(s)\n", failures);
        return 1;
    }
    printf("ok\n");
    return 0;
}
