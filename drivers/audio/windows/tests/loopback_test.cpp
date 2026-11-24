// User-mode tests of the driver's loopback ring (src\Inc\loopback.h).
// build.ps1 compiles and runs them before it builds the driver.

#include <cstdio>
#include <cstring>
#include <vector>

#include "loopback.h"

static int g_failures = 0;

#define CHECK(cond)                                                         \
    do                                                                      \
    {                                                                       \
        if (!(cond))                                                        \
        {                                                                   \
            std::printf("  FAIL %s:%d: %s\n", __FILE__, __LINE__, #cond);   \
            g_failures++;                                                   \
        }                                                                   \
    } while (0)

static const int kWriter = 1;
static const int kOtherWriter = 2;

struct Dma
{
    std::vector<unsigned char> bytes;
    PLANCK_DMA_VIEW view;

    Dma(unsigned long frames, unsigned short channels) : bytes(frames * 2u * channels, 0xAB)
    {
        view.Base = bytes.data();
        view.Bytes = (unsigned long)bytes.size();
        view.Channels = channels;
    }

    short Sample(unsigned long frame, unsigned short channel) const
    {
        return PlanckLoopbackLoad(&bytes[(frame * view.Channels + channel) * 2]);
    }

    void SetSample(unsigned long frame, unsigned short channel, short v)
    {
        PlanckLoopbackStore(&bytes[(frame * view.Channels + channel) * 2], v);
    }
};

static PLANCK_LOOPBACK_RING* NewRing()
{
    static PLANCK_LOOPBACK_RING ring;
    std::memset(&ring, 0, sizeof(ring));
    return &ring;
}

// Writes frames [first, first+count) of a ramp, where frame n is (n, -n) in
// stereo or n in mono, through a render buffer of dmaFrames, one buffer's
// worth at a time as the driver would.
static void WriteRamp(PLANCK_LOOPBACK_RING* ring, unsigned short channels, unsigned long first, unsigned long count, unsigned long dmaFrames = 480)
{
    Dma dma(dmaFrames, channels);
    unsigned long blockAlign = 2ul * channels;
    while (count > 0)
    {
        unsigned long chunk = count < dmaFrames ? count : dmaFrames;
        unsigned long long offset = (unsigned long long)first * blockAlign;
        for (unsigned long i = 0; i < chunk; i++)
        {
            unsigned long frame = (first + i) % dmaFrames;
            dma.SetSample(frame, 0, (short)(first + i));
            if (channels == 2)
            {
                dma.SetSample(frame, 1, (short)-(short)(first + i));
            }
        }
        PlanckLoopbackWrite(ring, &kWriter, &dma.view, offset, chunk);
        first += chunk;
        count -= chunk;
    }
}

static void TestMonoToStereo()
{
    std::printf("mono render -> stereo capture\n");
    PLANCK_LOOPBACK_RING* ring = NewRing();
    PlanckLoopbackStartWriter(ring, &kWriter);
    WriteRamp(ring, 1, 0, 2000);
    Dma cap(480, 2);
    unsigned long long cursor = 2000 - 480;
    CHECK(PlanckLoopbackRead(ring, &cursor, &cap.view, 0, 480) == 480);
    for (unsigned long f = 0; f < 480; f++)
    {
        CHECK(cap.Sample(f, 0) == (short)(1520 + f));
        CHECK(cap.Sample(f, 1) == (short)(1520 + f));
    }
    CHECK(cursor == 2000);
}

static void TestStereoToMono()
{
    std::printf("stereo render -> mono capture\n");
    PLANCK_LOOPBACK_RING* ring = NewRing();
    PlanckLoopbackStartWriter(ring, &kWriter);
    Dma ren(4, 2);
    short in[4][2] = {{100, 300}, {-32768, -32768}, {32767, 32767}, {32767, -32768}};
    for (unsigned long f = 0; f < 4; f++)
    {
        ren.SetSample(f, 0, in[f][0]);
        ren.SetSample(f, 1, in[f][1]);
    }
    PlanckLoopbackWrite(ring, &kWriter, &ren.view, 0, 4);
    Dma cap(4, 1);
    unsigned long long cursor = 0;
    CHECK(PlanckLoopbackRead(ring, &cursor, &cap.view, 0, 4) == 4);
    CHECK(cap.Sample(0, 0) == 200);
    CHECK(cap.Sample(1, 0) == -32768);
    CHECK(cap.Sample(2, 0) == 32767);
    CHECK(cap.Sample(3, 0) == 0);
}

static void TestStereoIdentityAcrossWraps()
{
    std::printf("stereo identity across both buffers' wraps\n");
    PLANCK_LOOPBACK_RING* ring = NewRing();
    PlanckLoopbackStartWriter(ring, &kWriter);
    // A 300-frame render buffer written in 128-frame steps and a 250-frame
    // capture buffer read in 96-frame steps both wrap many times.
    Dma ren(300, 2);
    Dma cap(250, 2);
    unsigned long long renPos = 0, capPos = 0, cursor = 0;
    unsigned long next = 0, expect = 0;
    bool ok = true;
    // The reader falls 32 frames further behind each step; 100 steps stay
    // under the lag limit, so every frame must come through in order.
    for (int step = 0; step < 100; step++)
    {
        for (unsigned long i = 0; i < 128; i++)
        {
            unsigned long frame = (unsigned long)((renPos / 4 + i) % 300);
            ren.SetSample(frame, 0, (short)next);
            ren.SetSample(frame, 1, (short)~next);
            next++;
        }
        PlanckLoopbackWrite(ring, &kWriter, &ren.view, renPos, 128);
        renPos += 128 * 4;
        unsigned long got = PlanckLoopbackRead(ring, &cursor, &cap.view, capPos, 96);
        if (got == 96)
        {
            for (unsigned long i = 0; i < 96; i++)
            {
                unsigned long frame = (unsigned long)((capPos / 4 + i) % 250);
                ok = ok && cap.Sample(frame, 0) == (short)expect && cap.Sample(frame, 1) == (short)~expect;
                expect++;
            }
        }
        CHECK(got == 96);
        capPos += 96 * 4;
    }
    CHECK(ok);
    CHECK(expect == 9600);
}

static void TestUnderrunHoldsPlace()
{
    std::printf("underrun: silence, nothing lost, order kept\n");
    PLANCK_LOOPBACK_RING* ring = NewRing();
    PlanckLoopbackStartWriter(ring, &kWriter);
    unsigned long long cursor = PLANCK_LOOPBACK_CURSOR_UNSET;
    Dma cap(480, 2);
    WriteRamp(ring, 2, 0, 200);
    CHECK(PlanckLoopbackRead(ring, &cursor, &cap.view, 0, 480) == 0);
    for (unsigned long f = 0; f < 480; f++)
    {
        CHECK(cap.Sample(f, 0) == 0 && cap.Sample(f, 1) == 0);
    }
    CHECK(cursor == 0);
    WriteRamp(ring, 2, 200, 400);
    CHECK(PlanckLoopbackRead(ring, &cursor, &cap.view, 0, 480) == 480);
    CHECK(cap.Sample(0, 0) == 0 && cap.Sample(479, 0) == 479 && cap.Sample(479, 1) == -479);
    CHECK(cursor == 480);
}

static void TestLagIsBounded()
{
    std::printf("a reader that falls behind skips ahead\n");
    PLANCK_LOOPBACK_RING* ring = NewRing();
    PlanckLoopbackStartWriter(ring, &kWriter);
    unsigned long long cursor = 0;
    WriteRamp(ring, 2, 0, 6000);
    Dma cap(480, 2);
    CHECK(PlanckLoopbackRead(ring, &cursor, &cap.view, 0, 480) == 480);
    CHECK(cursor == 6000 - PLANCK_LOOPBACK_RESYNC_FRAMES);
    CHECK(cap.Sample(0, 0) == (short)(6000 - PLANCK_LOOPBACK_RESYNC_FRAMES - 480));

    unsigned long long behind = 6000 - 480 - PLANCK_LOOPBACK_MAX_LAG_FRAMES;
    cursor = behind;
    CHECK(PlanckLoopbackRead(ring, &cursor, &cap.view, 0, 480) == 480);
    CHECK(cursor == behind + 480);   // exactly at the limit: kept
}

static void TestNoWriterIsSilentAndForgetsOldAudio()
{
    std::printf("no writer: silence, and old audio never replays\n");
    PLANCK_LOOPBACK_RING* ring = NewRing();
    PlanckLoopbackStartWriter(ring, &kWriter);
    WriteRamp(ring, 2, 0, 3000);
    PlanckLoopbackStopWriter(ring, &kWriter);
    Dma cap(480, 2);
    unsigned long long cursor = PLANCK_LOOPBACK_CURSOR_UNSET;
    CHECK(PlanckLoopbackRead(ring, &cursor, &cap.view, 0, 480) == 0);
    CHECK(cap.Sample(0, 0) == 0);

    // The writer comes back; a fresh reader must not resync into the
    // 3000 frames written before it left.
    PlanckLoopbackStartWriter(ring, &kWriter);
    CHECK(ring->WriterStart == 3000);
    WriteRamp(ring, 2, 3000, 600);
    cursor = PLANCK_LOOPBACK_CURSOR_UNSET;
    CHECK(PlanckLoopbackRead(ring, &cursor, &cap.view, 0, 480) == 480);
    CHECK(cap.Sample(0, 0) == 3000);
}

static void TestOneWriter()
{
    std::printf("a second render stream cannot write\n");
    PLANCK_LOOPBACK_RING* ring = NewRing();
    PlanckLoopbackStartWriter(ring, &kWriter);
    PlanckLoopbackStartWriter(ring, &kOtherWriter);
    CHECK(ring->Writer == &kWriter);
    Dma ren(10, 2);
    PlanckLoopbackWrite(ring, &kOtherWriter, &ren.view, 0, 10);
    CHECK(ring->Written == 0);
    PlanckLoopbackStopWriter(ring, &kOtherWriter);
    CHECK(ring->Writer == &kWriter);
    PlanckLoopbackWrite(ring, 0, &ren.view, 0, 10);
    CHECK(ring->Written == 0);
}

static void TestBadViewsAreRefused()
{
    std::printf("invalid buffers are never touched\n");
    PLANCK_LOOPBACK_RING* ring = NewRing();
    PlanckLoopbackStartWriter(ring, &kWriter);
    WriteRamp(ring, 2, 0, 2000);
    unsigned char guard[64];
    std::memset(guard, 0xCD, sizeof(guard));
    PLANCK_DMA_VIEW bad[] = {
        {0, 64, 2},          // no buffer
        {guard, 64, 3},      // three channels
        {guard, 64, 0},      // no channels
        {guard, 2, 2},       // smaller than a frame
        {guard, 62, 2},      // not a whole number of frames
    };
    for (const PLANCK_DMA_VIEW& view : bad)
    {
        unsigned long long cursor = 0;
        CHECK(PlanckLoopbackRead(ring, &cursor, &view, 0, 8) == 0);
        CHECK(cursor == 0);
        unsigned long long written = ring->Written;
        PlanckLoopbackWrite(ring, &kWriter, &view, 0, 8);
        CHECK(ring->Written == written);
    }
    for (unsigned char b : guard)
    {
        CHECK(b == 0xCD);
    }
}

static void TestHugeWriteKeepsNewest()
{
    std::printf("a write larger than the ring keeps the newest frames\n");
    PLANCK_LOOPBACK_RING* ring = NewRing();
    PlanckLoopbackStartWriter(ring, &kWriter);
    unsigned long frames = PLANCK_LOOPBACK_RING_FRAMES + 1000;
    Dma ren(frames, 1);
    for (unsigned long f = 0; f < frames; f++)
    {
        ren.SetSample(f, 0, (short)f);
    }
    PlanckLoopbackWrite(ring, &kWriter, &ren.view, 0, frames);
    CHECK(ring->Written == frames);
    Dma cap(16, 1);
    unsigned long long cursor = frames - 16;
    CHECK(PlanckLoopbackRead(ring, &cursor, &cap.view, 0, 16) == 16);
    CHECK(cap.Sample(15, 0) == (short)(frames - 1));
}

static void TestHugeReadIsSilent()
{
    std::printf("a read too large for the ring is silence\n");
    PLANCK_LOOPBACK_RING* ring = NewRing();
    PlanckLoopbackStartWriter(ring, &kWriter);
    WriteRamp(ring, 2, 0, 2000);
    unsigned long frames = PLANCK_LOOPBACK_RING_FRAMES;
    Dma cap(frames, 2);
    unsigned long long cursor = 0;
    CHECK(PlanckLoopbackRead(ring, &cursor, &cap.view, 0, frames) == 0);
    CHECK(cursor == 2000);
    CHECK(cap.Sample(frames - 1, 1) == 0);
}

static void TestUnalignedOffsets()
{
    std::printf("offsets snap to frames and wrap past the buffer\n");
    PLANCK_LOOPBACK_RING* ring = NewRing();
    PlanckLoopbackStartWriter(ring, &kWriter);
    Dma ren(8, 2);
    ren.SetSample(1, 0, 11);
    ren.SetSample(1, 1, 12);
    // 8 frames of 4 bytes = 32 bytes; offset 32*5 + 6 lands mid-frame 1.
    PlanckLoopbackWrite(ring, &kWriter, &ren.view, 32ull * 5 + 6, 1);
    Dma cap(8, 2);
    unsigned long long cursor = 0;
    CHECK(PlanckLoopbackRead(ring, &cursor, &cap.view, 32ull * 7 + 13, 1) == 1);
    CHECK(cap.Sample(3, 0) == 11 && cap.Sample(3, 1) == 12);
}

int main()
{
    TestMonoToStereo();
    TestStereoToMono();
    TestStereoIdentityAcrossWraps();
    TestUnderrunHoldsPlace();
    TestLagIsBounded();
    TestNoWriterIsSilentAndForgetsOldAudio();
    TestOneWriter();
    TestBadViewsAreRefused();
    TestHugeWriteKeepsNewest();
    TestHugeReadIsSilent();
    TestUnalignedOffsets();
    if (g_failures != 0)
    {
        std::printf("%d check(s) failed\n", g_failures);
        return 1;
    }
    std::printf("all loopback tests passed\n");
    return 0;
}
