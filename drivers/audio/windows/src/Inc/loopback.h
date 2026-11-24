/*++

Copyright (c) 2026 Planck contributors. MIT License; see ..\..\LICENSE.

Module Name:

    loopback.h

Abstract:

    The ring that carries audio from the Planck Virtual Audio speaker (render)
    pin to its microphone (capture) pin. Both pins run at 48 kHz, 16-bit PCM;
    each side may be mono or stereo and the ring stores stereo, so any pairing
    of channel counts works.

    This header makes no OS calls and takes no locks: the driver serializes
    calls with a spin lock (loopback.cpp), and the user-mode tests in
    tests\loopback_test.cpp compile these same functions.
--*/

#ifndef _PLANCK_LOOPBACK_H_
#define _PLANCK_LOOPBACK_H_

#define PLANCK_LOOPBACK_SAMPLE_RATE     48000u
#define PLANCK_LOOPBACK_BITS            16u
#define PLANCK_LOOPBACK_RING_FRAMES     8192u   // power of two, about 170 ms
#define PLANCK_LOOPBACK_MAX_LAG_FRAMES  3840u   // 80 ms: older audio is dropped
#define PLANCK_LOOPBACK_RESYNC_FRAMES   960u    // 20 ms: lag kept after a resync
#define PLANCK_LOOPBACK_CURSOR_UNSET    0xFFFFFFFFFFFFFFFFull

typedef struct _PLANCK_LOOPBACK_RING
{
    short               Samples[PLANCK_LOOPBACK_RING_FRAMES * 2];
    unsigned long long  Written;        // frames ever written
    unsigned long long  WriterStart;    // Written when the current writer started
    const void*         Writer;         // the render stream feeding the ring, or NULL
} PLANCK_LOOPBACK_RING;

// A WaveRT cyclic buffer of 16-bit PCM.
typedef struct _PLANCK_DMA_VIEW
{
    unsigned char*      Base;
    unsigned long       Bytes;
    unsigned short      Channels;
} PLANCK_DMA_VIEW;

inline bool PlanckLoopbackValidView(const PLANCK_DMA_VIEW* View)
{
    if (View == 0 || View->Base == 0 || (View->Channels != 1 && View->Channels != 2))
    {
        return false;
    }
    unsigned long blockAlign = 2ul * View->Channels;
    return View->Bytes >= blockAlign && View->Bytes % blockAlign == 0;
}

inline short PlanckLoopbackLoad(const unsigned char* P)
{
    return (short)(unsigned short)(P[0] | (P[1] << 8));
}

inline void PlanckLoopbackStore(unsigned char* P, short V)
{
    P[0] = (unsigned char)((unsigned short)V & 0xFF);
    P[1] = (unsigned char)((unsigned short)V >> 8);
}

// The frame-aligned offset of Offset within View.
inline unsigned long PlanckLoopbackFrameOffset(const PLANCK_DMA_VIEW* View, unsigned long long Offset)
{
    unsigned long blockAlign = 2ul * View->Channels;
    unsigned long offset = (unsigned long)(Offset % View->Bytes);
    return offset - offset % blockAlign;
}

inline void PlanckLoopbackStartWriter(PLANCK_LOOPBACK_RING* Ring, const void* Writer)
{
    if (Ring->Writer == 0)
    {
        Ring->Writer = Writer;
        Ring->WriterStart = Ring->Written;
    }
}

inline void PlanckLoopbackStopWriter(PLANCK_LOOPBACK_RING* Ring, const void* Writer)
{
    if (Ring->Writer == Writer)
    {
        Ring->Writer = 0;
    }
}

// Copies Frames frames of the render buffer, starting at byte Offset, into
// the ring. Only the stream that holds the writer slot writes.
inline void PlanckLoopbackWrite(
    PLANCK_LOOPBACK_RING*   Ring,
    const void*             Writer,
    const PLANCK_DMA_VIEW*  View,
    unsigned long long      Offset,
    unsigned long           Frames)
{
    if (Ring->Writer != Writer || Writer == 0 || !PlanckLoopbackValidView(View))
    {
        return;
    }
    unsigned long blockAlign = 2ul * View->Channels;
    unsigned long offset = PlanckLoopbackFrameOffset(View, Offset);
    if (Frames > PLANCK_LOOPBACK_RING_FRAMES)
    {
        unsigned long skip = Frames - PLANCK_LOOPBACK_RING_FRAMES;
        offset = PlanckLoopbackFrameOffset(View, (unsigned long long)offset + (unsigned long long)skip * blockAlign);
        Ring->Written += skip;
        Frames = PLANCK_LOOPBACK_RING_FRAMES;
    }
    for (unsigned long i = 0; i < Frames; i++)
    {
        const unsigned char* frame = View->Base + offset;
        short left = PlanckLoopbackLoad(frame);
        short right = View->Channels == 2 ? PlanckLoopbackLoad(frame + 2) : left;
        unsigned long slot = (unsigned long)(Ring->Written & (PLANCK_LOOPBACK_RING_FRAMES - 1));
        Ring->Samples[2 * slot] = left;
        Ring->Samples[2 * slot + 1] = right;
        Ring->Written++;
        offset += blockAlign;
        if (offset == View->Bytes)
        {
            offset = 0;
        }
    }
}

inline void PlanckLoopbackSilence(const PLANCK_DMA_VIEW* View, unsigned long Offset, unsigned long Frames)
{
    unsigned long blockAlign = 2ul * View->Channels;
    for (unsigned long i = 0; i < Frames; i++)
    {
        for (unsigned long b = 0; b < blockAlign; b++)
        {
            View->Base[Offset + b] = 0;
        }
        Offset += blockAlign;
        if (Offset == View->Bytes)
        {
            Offset = 0;
        }
    }
}

// Fills Frames frames of the capture buffer, starting at byte Offset, from
// the ring at *Cursor, and returns how many frames carried audio; the rest
// are silence. A reader that would run past the writer gets silence and
// keeps its place, which builds a cushion against timer jitter; one that
// falls more than PLANCK_LOOPBACK_MAX_LAG_FRAMES behind skips ahead.
inline unsigned long PlanckLoopbackRead(
    PLANCK_LOOPBACK_RING*   Ring,
    unsigned long long*     Cursor,
    const PLANCK_DMA_VIEW*  View,
    unsigned long long      Offset,
    unsigned long           Frames)
{
    if (!PlanckLoopbackValidView(View))
    {
        return 0;
    }
    unsigned long offset = PlanckLoopbackFrameOffset(View, Offset);
    unsigned long long written = Ring->Written;

    if (Ring->Writer == 0 || Frames > PLANCK_LOOPBACK_RING_FRAMES - PLANCK_LOOPBACK_MAX_LAG_FRAMES)
    {
        *Cursor = written;
        PlanckLoopbackSilence(View, offset, Frames);
        return 0;
    }

    unsigned long long oldest = Ring->WriterStart;
    if (*Cursor == PLANCK_LOOPBACK_CURSOR_UNSET || *Cursor > written ||
        written - *Cursor > (unsigned long long)Frames + PLANCK_LOOPBACK_MAX_LAG_FRAMES)
    {
        unsigned long long lag = (unsigned long long)Frames + PLANCK_LOOPBACK_RESYNC_FRAMES;
        *Cursor = written > lag ? written - lag : 0;
    }
    if (*Cursor < oldest)
    {
        *Cursor = oldest;
    }
    if (written - *Cursor < Frames)
    {
        PlanckLoopbackSilence(View, offset, Frames);
        return 0;
    }

    unsigned long blockAlign = 2ul * View->Channels;
    for (unsigned long i = 0; i < Frames; i++)
    {
        unsigned long slot = (unsigned long)(*Cursor & (PLANCK_LOOPBACK_RING_FRAMES - 1));
        short left = Ring->Samples[2 * slot];
        short right = Ring->Samples[2 * slot + 1];
        unsigned char* frame = View->Base + offset;
        if (View->Channels == 2)
        {
            PlanckLoopbackStore(frame, left);
            PlanckLoopbackStore(frame + 2, right);
        }
        else
        {
            PlanckLoopbackStore(frame, (short)(((int)left + (int)right) / 2));
        }
        (*Cursor)++;
        offset += blockAlign;
        if (offset == View->Bytes)
        {
            offset = 0;
        }
    }
    return Frames;
}

#ifdef _KERNEL_MODE
// loopback.cpp: the driver's ring, behind a spin lock.
_IRQL_requires_(PASSIVE_LEVEL)
VOID PlanckLoopbackInitialize();

_IRQL_requires_max_(DISPATCH_LEVEL)
VOID PlanckLoopbackAttachWriter(_In_ const VOID* Writer);

_IRQL_requires_max_(DISPATCH_LEVEL)
VOID PlanckLoopbackDetachWriter(_In_ const VOID* Writer);

_IRQL_requires_max_(DISPATCH_LEVEL)
VOID PlanckLoopbackRender(_In_ const VOID* Writer, _In_ const PLANCK_DMA_VIEW* View, _In_ ULONGLONG Offset, _In_ ULONG Frames);

_IRQL_requires_max_(DISPATCH_LEVEL)
VOID PlanckLoopbackCapture(_Inout_ ULONGLONG* Cursor, _In_ const PLANCK_DMA_VIEW* View, _In_ ULONGLONG Offset, _In_ ULONG Frames);
#endif

#endif // _PLANCK_LOOPBACK_H_
