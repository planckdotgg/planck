/*++

Copyright (c) Microsoft Corporation All Rights Reserved

Module Name:

    speakerwavtable.h

Abstract:

    Declaration of wave miniport tables for the render endpoints.
--*/

#ifndef _VIRTUALAUDIODRIVER_SPEAKERWAVTABLE_H_
#define _VIRTUALAUDIODRIVER_SPEAKERWAVTABLE_H_

#include "loopback.h"

//=============================================================================
// Planck locks the speaker pin to 48 kHz 16-bit PCM, mono or stereo: the formats
// the loopback ring carries (loopback.h). No 44.1 kHz format exists, so the
// speaker and microphone sides can never disagree on rate.
//
#define SPEAKER_DEVICE_MAX_CHANNELS         2

//
// Max # of pin instances.
//
#define SPEAKER_MAX_INPUT_SYSTEM_STREAMS    1

static
KSDATAFORMAT_WAVEFORMATEXTENSIBLE SpeakerHostPinSupportedDeviceFormats[] =
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
MODE_AND_DEFAULT_FORMAT SpeakerHostPinSupportedDeviceModes[] =
{
    {
        STATIC_AUDIO_SIGNALPROCESSINGMODE_DEFAULT,
        &SpeakerHostPinSupportedDeviceFormats[0].DataFormat
    }
};

// The entries must follow the same order as the filter's pin descriptor array.
static
PIN_DEVICE_FORMATS_AND_MODES SpeakerPinDeviceFormatsAndModes[] =
{
    {
        SystemRenderPin,
        SpeakerHostPinSupportedDeviceFormats,
        SIZEOF_ARRAY(SpeakerHostPinSupportedDeviceFormats),
        SpeakerHostPinSupportedDeviceModes,
        SIZEOF_ARRAY(SpeakerHostPinSupportedDeviceModes)
    },
    {
        BridgePin,
        NULL,
        0,
        NULL,
        0
    }
};

//=============================================================================
static
KSDATARANGE_AUDIO SpeakerPinDataRangesStream[] =
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
PKSDATARANGE SpeakerPinDataRangePointersStream[] =
{
    PKSDATARANGE(&SpeakerPinDataRangesStream[0]),
    PKSDATARANGE(&PinDataRangeAttributeList),
};

//=============================================================================
static
KSDATARANGE SpeakerPinDataRangesBridge[] =
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
PKSDATARANGE SpeakerPinDataRangePointersBridge[] =
{
    &SpeakerPinDataRangesBridge[0]
};

//=============================================================================
static
PCPIN_DESCRIPTOR SpeakerWaveMiniportPins[] =
{
    // Wave Out Streaming Pin (Renderer) KSPIN_WAVE_RENDER3_SINK_SYSTEM
    {
        SPEAKER_MAX_INPUT_SYSTEM_STREAMS,
        SPEAKER_MAX_INPUT_SYSTEM_STREAMS,
        0,
        NULL,        // AutomationTable
        {
            0,
            NULL,
            0,
            NULL,
            SIZEOF_ARRAY(SpeakerPinDataRangePointersStream),
            SpeakerPinDataRangePointersStream,
            KSPIN_DATAFLOW_IN,
            KSPIN_COMMUNICATION_SINK,
            &KSCATEGORY_AUDIO,
            NULL,
            0
        }
    },
    // Wave Out Bridge Pin (Renderer) KSPIN_WAVE_RENDER3_SOURCE
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
            SIZEOF_ARRAY(SpeakerPinDataRangePointersBridge),
            SpeakerPinDataRangePointersBridge,
            KSPIN_DATAFLOW_OUT,
            KSPIN_COMMUNICATION_NONE,
            &KSCATEGORY_AUDIO,
            NULL,
            0
        }
    },
};

//=============================================================================
//
//                   ----------------------------      
//                   |                          |      
//  Host Pin     0-->|                          |--> 1 KSPIN_WAVE_RENDER3_SOURCE
//                   |                          |      
//                   ----------------------------
//
static
PCCONNECTION_DESCRIPTOR SpeakerWaveMiniportConnections[] =
{
    { PCFILTER_NODE, KSPIN_WAVE_RENDER3_SINK_SYSTEM, PCFILTER_NODE, KSPIN_WAVE_RENDER3_SOURCE }
};

//=============================================================================
static
PCPROPERTY_ITEM PropertiesSpeakerWaveFilter[] =
{
    {
        &KSPROPSETID_Pin,
        KSPROPERTY_PIN_PROPOSEDATAFORMAT,
        KSPROPERTY_TYPE_SET | KSPROPERTY_TYPE_BASICSUPPORT,
        PropertyHandler_WaveFilter
    },
    {
        &KSPROPSETID_Pin,
        KSPROPERTY_PIN_PROPOSEDATAFORMAT2,
        KSPROPERTY_TYPE_GET | KSPROPERTY_TYPE_BASICSUPPORT,
        PropertyHandler_WaveFilter
    }
};

DEFINE_PCAUTOMATION_TABLE_PROP(AutomationSpeakerWaveFilter, PropertiesSpeakerWaveFilter);

//=============================================================================
static
PCFILTER_DESCRIPTOR SpeakerWaveMiniportFilterDescriptor =
{
    0,                                              // Version
    &AutomationSpeakerWaveFilter,                   // AutomationTable
    sizeof(PCPIN_DESCRIPTOR),                       // PinSize
    SIZEOF_ARRAY(SpeakerWaveMiniportPins),          // PinCount
    SpeakerWaveMiniportPins,                        // Pins
    sizeof(PCNODE_DESCRIPTOR),                      // NodeSize
    0,                                              // NodeCount
    NULL,                                           // Nodes
    SIZEOF_ARRAY(SpeakerWaveMiniportConnections),   // ConnectionCount
    SpeakerWaveMiniportConnections,                 // Connections
    0,                                              // CategoryCount
    NULL                                            // Categories  - use defaults (audio, render, capture)
};

#endif // _VIRTUALAUDIODRIVER_SPEAKERWAVTABLE_H_
