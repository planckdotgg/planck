/*++

Copyright (c) 2026 Planck contributors. MIT License; see ..\..\LICENSE.

Module Name:

    loopback.cpp

Abstract:

    The driver's one loopback ring. It is static non-paged data, so nothing is
    allocated or freed and teardown cannot leak it. Every call holds
    g_LoopbackLock; callers may run at PASSIVE_LEVEL or DISPATCH_LEVEL (the
    stream position spin lock is held around reads and writes), so the lock
    is always taken with KeAcquireSpinLock, and only ever after the stream
    lock, never before it.
--*/

#include "definitions.h"
#include "loopback.h"

#pragma code_seg()

static PLANCK_LOOPBACK_RING g_Loopback;
static KSPIN_LOCK           g_LoopbackLock;

_IRQL_requires_(PASSIVE_LEVEL)
VOID PlanckLoopbackInitialize()
{
    KeInitializeSpinLock(&g_LoopbackLock);
    RtlZeroMemory(&g_Loopback, sizeof(g_Loopback));
}

_IRQL_requires_max_(DISPATCH_LEVEL)
VOID PlanckLoopbackAttachWriter(_In_ const VOID* Writer)
{
    KIRQL irql;
    KeAcquireSpinLock(&g_LoopbackLock, &irql);
    PlanckLoopbackStartWriter(&g_Loopback, Writer);
    KeReleaseSpinLock(&g_LoopbackLock, irql);
}

_IRQL_requires_max_(DISPATCH_LEVEL)
VOID PlanckLoopbackDetachWriter(_In_ const VOID* Writer)
{
    KIRQL irql;
    KeAcquireSpinLock(&g_LoopbackLock, &irql);
    PlanckLoopbackStopWriter(&g_Loopback, Writer);
    KeReleaseSpinLock(&g_LoopbackLock, irql);
}

_IRQL_requires_max_(DISPATCH_LEVEL)
VOID PlanckLoopbackRender
(
    _In_ const VOID*            Writer,
    _In_ const PLANCK_DMA_VIEW* View,
    _In_ ULONGLONG              Offset,
    _In_ ULONG                  Frames
)
{
    KIRQL irql;
    KeAcquireSpinLock(&g_LoopbackLock, &irql);
    PlanckLoopbackWrite(&g_Loopback, Writer, View, Offset, Frames);
    KeReleaseSpinLock(&g_LoopbackLock, irql);
}

_IRQL_requires_max_(DISPATCH_LEVEL)
VOID PlanckLoopbackCapture
(
    _Inout_ ULONGLONG*              Cursor,
    _In_    const PLANCK_DMA_VIEW*  View,
    _In_    ULONGLONG               Offset,
    _In_    ULONG                   Frames
)
{
    KIRQL irql;
    KeAcquireSpinLock(&g_LoopbackLock, &irql);
    PlanckLoopbackRead(&g_Loopback, Cursor, View, Offset, Frames);
    KeReleaseSpinLock(&g_LoopbackLock, irql);
}
