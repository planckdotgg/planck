/*++

Copyright (c) Microsoft Corporation All Rights Reserved

Module Name:

    micarraywavtable.h

Abstract:-

    Declaration of wave miniport tables for the mic array.

--*/

#ifndef _VIRTUALAUDIODRIVER_MICARRAYWAVTABLE_H_
#define _VIRTUALAUDIODRIVER_MICARRAYWAVTABLE_H_

#include "loopback.h"

//=============================================================================
// Planck locks the microphone pin to 48 kHz 16-bit PCM, mono or stereo: the formats
// the loopback ring carries (loopback.h). No 44.1 kHz format exists, so the
// speaker and microphone sides can never disagree on rate.
//
#define MICARRAY_DEVICE_MAX_CHANNELS         2

//
// Max # of pin instances.
//
#define MICARRAY_MAX_INPUT_STREAMS    1

static
KSDATAFORMAT_WAVEFORMATEXTENSIBLE MicArrayPinSupportedDeviceFormats[] =
{
    // 0) 48 kHz, 16-bit, stereo: the default.
    {
        {
            sizeof(KSDATAFORMAT_WAVEFORMATEXTENSIBLE),
            0,
            0,
            0,
            STATICGUIDOF(KSDATAFORMAT_TYPE_AUDIO),
            STATICGUIDOF(KSDATAFORMAT_SUBTYPE_PCM),
            STATICGUIDOF(KSDATAFORMAT_SPECIFIER_WAVEFORMATEX)
        },
        {
            {
                WAVE_FORMAT_EXTENSIBLE,
                2,
                PLANCK_LOOPBACK_SAMPLE_RATE,
                PLANCK_LOOPBACK_SAMPLE_RATE * 2 * PLANCK_LOOPBACK_BITS / 8,
                2 * PLANCK_LOOPBACK_BITS / 8,
                PLANCK_LOOPBACK_BITS,
                sizeof(WAVEFORMATEXTENSIBLE) - sizeof(WAVEFORMATEX)
            },
            PLANCK_LOOPBACK_BITS,
            KSAUDIO_SPEAKER_STEREO,
            STATICGUIDOF(KSDATAFORMAT_SUBTYPE_PCM)
        }
    },
    // 1) 48 kHz, 16-bit, mono.
    {
        {
            sizeof(KSDATAFORMAT_WAVEFORMATEXTENSIBLE),
            0,
            0,
            0,
            STATICGUIDOF(KSDATAFORMAT_TYPE_AUDIO),
            STATICGUIDOF(KSDATAFORMAT_SUBTYPE_PCM),
            STATICGUIDOF(KSDATAFORMAT_SPECIFIER_WAVEFORMATEX)
        },
        {
            {
                WAVE_FORMAT_EXTENSIBLE,
                1,
                PLANCK_LOOPBACK_SAMPLE_RATE,
                PLANCK_LOOPBACK_SAMPLE_RATE * PLANCK_LOOPBACK_BITS / 8,
                PLANCK_LOOPBACK_BITS / 8,
                PLANCK_LOOPBACK_BITS,
                sizeof(WAVEFORMATEXTENSIBLE) - sizeof(WAVEFORMATEX)
            },
            PLANCK_LOOPBACK_BITS,
            KSAUDIO_SPEAKER_MONO,
            STATICGUIDOF(KSDATAFORMAT_SUBTYPE_PCM)
        }
    }
};

static
MODE_AND_DEFAULT_FORMAT MicArrayPinSupportedDeviceModes[] =
{
    {
        STATIC_AUDIO_SIGNALPROCESSINGMODE_RAW,
        &MicArrayPinSupportedDeviceFormats[0].DataFormat
    }
};

//
// The entries here must follow the same order as the filter's pin
// descriptor array.
//
static
PIN_DEVICE_FORMATS_AND_MODES MicArrayPinDeviceFormatsAndModes[] =
{
    {
        BridgePin,
        NULL,
        0,
        NULL,
        0
    },
    {
        SystemCapturePin,
        MicArrayPinSupportedDeviceFormats,
        SIZEOF_ARRAY(MicArrayPinSupportedDeviceFormats),
        MicArrayPinSupportedDeviceModes,
        SIZEOF_ARRAY(MicArrayPinSupportedDeviceModes)
    }
};

//=============================================================================
// Data ranges
//
// See CMiniportWaveRT::DataRangeIntersection.
//
static
KSDATARANGE_AUDIO MicArrayPinDataRangesRawStream[] =
{
    {
        {
            sizeof(KSDATARANGE_AUDIO),
            KSDATARANGE_ATTRIBUTES,         // An attributes list follows this data range
            0,
            0,
            STATICGUIDOF(KSDATAFORMAT_TYPE_AUDIO),
            STATICGUIDOF(KSDATAFORMAT_SUBTYPE_PCM),
            STATICGUIDOF(KSDATAFORMAT_SPECIFIER_WAVEFORMATEX)
        },
        2,                              // MaximumChannels: mono or stereo
        PLANCK_LOOPBACK_BITS,           // MinimumBitsPerSample
        PLANCK_LOOPBACK_BITS,           // MaximumBitsPerSample
        PLANCK_LOOPBACK_SAMPLE_RATE,    // MinimumSampleFrequency
        PLANCK_LOOPBACK_SAMPLE_RATE     // MaximumSampleFrequency
    }
};

static
PKSDATARANGE MicArrayPinDataRangePointersStream[] =
{
    // All supported device formats should be listed in the DataRange.
    PKSDATARANGE(&MicArrayPinDataRangesRawStream[0]),
    PKSDATARANGE(&PinDataRangeAttributeList),
};

//=============================================================================
static
KSDATARANGE MicArrayPinDataRangesBridge[] =
{
    {
        sizeof(KSDATARANGE),
        0,
        0,
        0,
        STATICGUIDOF(KSDATAFORMAT_TYPE_AUDIO),
        STATICGUIDOF(KSDATAFORMAT_SUBTYPE_ANALOG),
        STATICGUIDOF(KSDATAFORMAT_SPECIFIER_NONE)
    }
};

static
PKSDATARANGE MicArrayPinDataRangePointersBridge[] =
{
    &MicArrayPinDataRangesBridge[0]
};

//=============================================================================
static
PCPIN_DESCRIPTOR MicArrayWaveMiniportPins[] =
{
    // Wave In Bridge Pin (Capture - From Topology) KSPIN_WAVE_BRIDGE
    {
        0,
        0,
        0,
        NULL,
        {
            0,
            NULL,
            0,
            NULL,
            SIZEOF_ARRAY(MicArrayPinDataRangePointersBridge),
            MicArrayPinDataRangePointersBridge,
            KSPIN_DATAFLOW_IN,
            KSPIN_COMMUNICATION_NONE,
            &KSCATEGORY_AUDIO,
            NULL,
            0
        }
    },
    // Wave In Streaming Pin (Capture) KSPIN_WAVE_HOST
    {
        MICARRAY_MAX_INPUT_STREAMS,
        MICARRAY_MAX_INPUT_STREAMS,
        0,
        NULL,
        {
            0,
            NULL,
            0,
            NULL,
            SIZEOF_ARRAY(MicArrayPinDataRangePointersStream),
            MicArrayPinDataRangePointersStream,
            KSPIN_DATAFLOW_OUT,
            KSPIN_COMMUNICATION_SINK,
            &KSCATEGORY_AUDIO,
            &KSAUDFNAME_RECORDING_CONTROL,
            0
        }
    }
};

//=============================================================================
static
PCNODE_DESCRIPTOR MicArrayWaveMiniportNodes[] =
{
    // KSNODE_WAVE_ADC
    {
        0,                      // Flags
        NULL,                   // AutomationTable
        &KSNODETYPE_ADC,        // Type
        NULL                    // Name
    }
};

//=============================================================================
static
PCCONNECTION_DESCRIPTOR MicArrayWaveMiniportConnections[] =
{
    { PCFILTER_NODE,        KSPIN_WAVE_BRIDGE,      KSNODE_WAVE_ADC,     1 },
    { KSNODE_WAVE_ADC,      0,                      PCFILTER_NODE,       KSPIN_WAVEIN_HOST },
};

//=============================================================================

static
VIRTUALAUDIODRIVER_PROPERTY_ITEM PropertiesMicArrayWaveFilter[] =
{
    {
        {
            &KSPROPSETID_General,
            KSPROPERTY_GENERAL_COMPONENTID,
            KSPROPERTY_TYPE_GET | KSPROPERTY_TYPE_BASICSUPPORT,
            PropertyHandler_WaveFilter,
        },
        0,
        0,
        NULL,
        NULL,
        NULL,
        NULL,
        0
    },
    {
        {
            &KSPROPSETID_Pin,
            KSPROPERTY_PIN_PROPOSEDATAFORMAT,
            KSPROPERTY_TYPE_SET | KSPROPERTY_TYPE_BASICSUPPORT,
            PropertyHandler_WaveFilter,
        },
        0,
        0,
        NULL,
        NULL,
        NULL,
        NULL,
        0
    },
    {
        {
            &KSPROPSETID_Pin,
            KSPROPERTY_PIN_PROPOSEDATAFORMAT2,
            KSPROPERTY_TYPE_GET | KSPROPERTY_TYPE_BASICSUPPORT,
            PropertyHandler_WaveFilter,
        },
        0,
        0,
        NULL,
        NULL,
        NULL,
        NULL,
        0
    },
};

DEFINE_PCAUTOMATION_TABLE_PROP(AutomationMicArrayWaveFilter, PropertiesMicArrayWaveFilter);

//=============================================================================
static
PCFILTER_DESCRIPTOR MicArrayWaveMiniportFilterDescriptor =
{
    0,                                              // Version
    &AutomationMicArrayWaveFilter,                  // AutomationTable
    sizeof(PCPIN_DESCRIPTOR),                       // PinSize
    SIZEOF_ARRAY(MicArrayWaveMiniportPins),         // PinCount
    MicArrayWaveMiniportPins,                       // Pins
    sizeof(PCNODE_DESCRIPTOR),                      // NodeSize
    SIZEOF_ARRAY(MicArrayWaveMiniportNodes),        // NodeCount
    MicArrayWaveMiniportNodes,                      // Nodes
    SIZEOF_ARRAY(MicArrayWaveMiniportConnections),  // ConnectionCount
    MicArrayWaveMiniportConnections,                // Connections
    0,                                              // CategoryCount
    NULL                                            // Categories  - use defaults (audio, render, capture)
};

#endif // _VIRTUALAUDIODRIVER_MICARRAYWAVTABLE_H_
