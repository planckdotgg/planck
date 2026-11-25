// Copyright (c) 2026 Planck contributors. Apache License 2.0; see LICENSE
// at the repository root.

#ifndef PLANCK_AUDIO_H
#define PLANCK_AUDIO_H

#include <CoreFoundation/CoreFoundation.h>

#define PLANCK_AUDIO_SAMPLE_RATE 48000.0
#define PLANCK_AUDIO_CHANNELS 2
// About 341 ms at 48 kHz: the loopback ring and the clock's period.
#define PLANCK_AUDIO_RING_FRAMES 16384
// pkg/audio finds the device by its name, "Planck Virtual Audio"; the UID
// keeps apps' saved device choice across restarts.
#define PLANCK_AUDIO_DEVICE_UID "gg.planck.audio.device"

// PlanckAudio_Create is the CFPlugIn factory named in Info.plist.
__attribute__((visibility("default"))) void* PlanckAudio_Create(CFAllocatorRef allocator, CFUUIDRef requestedType);

// The loopback ring, called only from the IO thread. They are exported to
// tests/loopback_test.c, which links the source directly.
void PlanckAudio_WriteRing(const Float32* in, UInt32 frames, Float64 sampleTime);
void PlanckAudio_ReadRing(Float32* out, UInt32 frames, Float64 sampleTime);

#endif
