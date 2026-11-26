package audio

import (
	"errors"
	"fmt"

	"github.com/jfreymuth/pulse"
	"github.com/jfreymuth/pulse/proto"
)

// outputDevices are the virtual mic candidates: the sound server's sinks,
// plus Planck's own null sink, made on first use (OpenMicSink).
func outputDevices() ([]VirtualMic, error) {
	sinks, err := sinkDevices()
	if err != nil {
		return nil, err
	}
	return micDevices(sinks), nil
}

func prepareVirtualMic(*VirtualMic) error { return nil }

// pulseMicSink plays a client's microphone into a sink whose monitor apps
// can record from.
type pulseMicSink struct {
	*pulsePlayer
	device  string
	client  *pulse.Client
	modules []uint32
}

func (m *pulseMicSink) Device() string { return m.device }

// OpenMicSink plays mono 48 kHz PCM into dev's sink. For Planck's own
// virtual device it first loads module-null-sink and module-virtual-source
// when the server does not have them, and unloads what it loaded on Close;
// apps then see a microphone named "Planck Virtual Microphone".
func OpenMicSink(dev VirtualMic) (MicSink, error) {
	c, err := newPulseClient()
	if err != nil {
		return nil, err
	}
	m := &pulseMicSink{client: c, device: dev.ID + ".monitor"}
	if dev.ID == virtualMicSink {
		if err := m.ensureVirtualSource(); err != nil {
			m.unload()
			c.Close()
			return nil, err
		}
		m.device = virtualMicSource
	}
	p, err := newPulsePlayer(dev.ID, MicChannels)
	if err != nil {
		m.unload()
		c.Close()
		return nil, err
	}
	m.pulsePlayer = p
	return m, nil
}

func (m *pulseMicSink) ensureVirtualSource() error {
	if _, err := m.client.SinkByID(virtualMicSink); err != nil {
		if err := m.load("module-null-sink", nullSinkArgs()); err != nil {
			return err
		}
	}
	if _, err := m.client.SourceByID(virtualMicSource); err != nil {
		if err := m.load("module-virtual-source", virtualSourceArgs()); err != nil {
			return err
		}
	}
	return nil
}

func (m *pulseMicSink) load(module, args string) error {
	var reply proto.LoadModuleReply
	if err := m.client.RawRequest(&proto.LoadModule{Name: module, Args: args}, &reply); err != nil {
		return fmt.Errorf("audio: load %s: %w", module, err)
	}
	m.modules = append(m.modules, reply.ModuleIndex)
	return nil
}

func (m *pulseMicSink) unload() error {
	var errs []error
	for i := len(m.modules) - 1; i >= 0; i-- {
		if err := m.client.RawRequest(&proto.UnloadModule{ModuleIndex: m.modules[i]}, nil); err != nil {
			errs = append(errs, fmt.Errorf("audio: unload module %d: %w", m.modules[i], err))
		}
	}
	m.modules = nil
	return errors.Join(errs...)
}

func (m *pulseMicSink) Close() error {
	err := m.pulsePlayer.Close()
	uerr := m.unload()
	m.client.Close()
	return errors.Join(err, uerr)
}

var _ MicSink = (*pulseMicSink)(nil)
