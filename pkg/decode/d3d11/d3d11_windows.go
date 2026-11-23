//go:build windows

// Package d3d11 decodes HEVC and AV1 on the GPU of a Direct3D 11 device
// through a Media Foundation decoder transform made aware of the device
// (DXVA). Pictures stay in the decoder's textures on that device.
package d3d11

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
	"time"
	"unsafe"

	"github.com/planckdotgg/planck/internal/clock"
	"github.com/planckdotgg/planck/internal/win"
	"github.com/planckdotgg/planck/pkg/capture"
	"github.com/planckdotgg/planck/pkg/decode"
	"github.com/planckdotgg/planck/pkg/encode"
	"github.com/planckdotgg/planck/pkg/gpu"
)

const attrGetBlob = 15

// Decoder is a hardware decoder on one D3D11 device.
type Decoder struct {
	name    string
	device  gpu.Device
	mft     obj
	manager obj
	cfg     decode.Config

	format        capture.Format
	width, height int

	// Per-frame out parameters, on the heap so the calls do not allocate.
	out     *[4]uintptr // MFT_OUTPUT_DATA_BUFFER
	status  *uint32
	sample  *obj
	buffer  *obj
	dxgi    *obj
	tex     *obj
	slice   *uint32
	locked  *uintptr
	pts     *int64
	pending int
	dropped int
}

// Decoders lists the names of the decoder transforms registered for codec,
// best first.
func Decoders(codec encode.Codec) ([]string, error) {
	sub, err := subtype(codec)
	if err != nil {
		return nil, err
	}
	list, err := enumDecoders(sub)
	if err != nil {
		return nil, err
	}
	var names []string
	for i := range list {
		names = append(names, list[i].name)
		list[i].activate.release()
	}
	return names, nil
}

func subtype(c encode.Codec) (win.GUID, error) {
	switch c {
	case encode.HEVC:
		return formatHEVC, nil
	case encode.AV1:
		return formatAV1, nil
	}
	return win.GUID{}, fmt.Errorf("%w: %s", decode.ErrUnsupported, c)
}

// Open makes a decoder for cfg.Codec on dev, whose Handle is an
// ID3D11Device* made with video support. Decoded textures belong to dev.
func Open(dev gpu.Device, cfg decode.Config) (*Decoder, error) {
	if dev.Handle == 0 {
		return nil, errors.New("d3d11: no device")
	}
	sub, err := subtype(cfg.Codec)
	if err != nil {
		return nil, err
	}
	list, err := enumDecoders(sub)
	if err != nil {
		return nil, err
	}
	defer func() {
		for i := range list {
			list[i].activate.release()
		}
	}()
	if len(list) == 0 {
		return nil, fmt.Errorf("%w: no %s decoder transform is installed", decode.ErrUnsupported, cfg.Codec)
	}
	var errs []string
	for _, m := range list {
		d := &Decoder{name: m.name, device: dev, cfg: cfg, out: new([4]uintptr), status: new(uint32),
			sample: new(obj), buffer: new(obj), dxgi: new(obj), tex: new(obj), slice: new(uint32), locked: new(uintptr), pts: new(int64)}
		if hr := m.activate.call3(activateObject, uintptr(unsafe.Pointer(&iidTransform)), uintptr(unsafe.Pointer(&d.mft)), 0); hr.Failed() {
			errs = append(errs, fmt.Sprintf("%s: ActivateObject %v", m.name, hr))
			continue
		}
		if err := d.setup(sub); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", m.name, err))
			d.Close()
			continue
		}
		return d, nil
	}
	return nil, fmt.Errorf("%w: %s on %s: %s", decode.ErrUnsupported, cfg.Codec, dev.Adapter.Name, strings.Join(errs, "; "))
}

// Name is the decoder transform's friendly name.
func (d *Decoder) Name() string { return d.name }

func (d *Decoder) setup(sub win.GUID) error {
	var attrs obj
	if hr := d.mft.call3(mftGetAttributes, uintptr(unsafe.Pointer(&attrs)), 0, 0); hr.Failed() {
		return fmt.Errorf("GetAttributes: %w", hr)
	}
	aware, _ := getUINT32(attrs, &attrD3D11Aware)
	setUINT32(attrs, &attrLowLatency, 1)
	attrs.release()
	if aware == 0 {
		return errors.New("not Direct3D 11 aware")
	}

	device := obj(d.device.Handle)
	if mt, err := device.queryInterface(&iidMultithread); err == nil {
		mt.call3(setMultithreadProtected, 1, 0, 0)
		mt.release()
	}
	var token uint32
	if r, _, _ := procMFCreateDXGIDeviceMgr.Call(uintptr(unsafe.Pointer(&token)), uintptr(unsafe.Pointer(&d.manager))); win.HRESULT(r).Failed() {
		return fmt.Errorf("MFCreateDXGIDeviceManager: %w", win.HRESULT(r))
	}
	if hr := d.manager.call3(mgrResetDevice, d.device.Handle, uintptr(token), 0); hr.Failed() {
		return fmt.Errorf("ResetDevice: %w", hr)
	}
	if hr := d.mft.call3(mftProcessMessage, msgSetD3DManager, uintptr(d.manager), 0); hr.Failed() {
		return fmt.Errorf("MFT_MESSAGE_SET_D3D_MANAGER: %w", hr)
	}
	var outAttrs obj
	if hr := d.mft.call3(mftGetOutputStreamAttributes, 0, uintptr(unsafe.Pointer(&outAttrs)), 0); !hr.Failed() {
		// Shader-readable textures, so the picture can be drawn as it is.
		setUINT32(outAttrs, &attrD3D11BindFlags, bindDecoder|bindShaderResource)
		outAttrs.release()
	}

	var in obj
	if r, _, _ := procMFCreateMediaType.Call(uintptr(unsafe.Pointer(&in))); win.HRESULT(r).Failed() {
		return fmt.Errorf("MFCreateMediaType: %w", win.HRESULT(r))
	}
	defer in.release()
	setGUID(in, &mtMajorType, &mediaTypeVideo)
	setGUID(in, &mtSubtype, &sub)
	setUINT32(in, &mtInterlaceMode, interlaceProgressive)
	// The decoder offers output types only for a known size, and takes no
	// input before one is set: without a hint, start from 1080p and let the
	// stream's own size arrive as a stream change.
	fw, fh := d.cfg.Width, d.cfg.Height
	if fw <= 0 || fh <= 0 {
		fw, fh = 1920, 1080
	}
	setUINT64(in, &mtFrameSize, uint64(fw)<<32|uint64(fh))
	if d.cfg.FPS > 0 {
		setUINT64(in, &mtFrameRate, uint64(d.cfg.FPS)<<32|1)
	}
	if hr := d.mft.call3(mftSetInputType, 0, uintptr(in), 0); hr.Failed() {
		return fmt.Errorf("SetInputType: %w", hr)
	}
	if err := d.chooseOutput(); err != nil {
		return err
	}
	d.mft.call3(mftProcessMessage, msgBeginStreaming, 0, 0)
	d.mft.call3(mftProcessMessage, msgStartOfStream, 0, 0)
	return nil
}

// chooseOutput picks NV12 or P010, whichever the stream's bit depth gives
// first, and reads the picture size.
func (d *Decoder) chooseOutput() error {
	for i := uintptr(0); ; i++ {
		var t obj
		hr := d.mft.call3(mftGetOutputAvailableType, 0, i, uintptr(unsafe.Pointer(&t)))
		if hr == eNoMoreTypes {
			return errors.New("no NV12 or P010 output")
		}
		if hr.Failed() {
			return fmt.Errorf("GetOutputAvailableType: %w", hr)
		}
		sub, _ := getGUID(t, &mtSubtype)
		if sub != formatNV12 && sub != formatP010 {
			t.release()
			continue
		}
		hr = d.mft.call3(mftSetOutputType, 0, uintptr(t), 0)
		t.release()
		if hr.Failed() {
			return fmt.Errorf("SetOutputType: %w", hr)
		}
		// MFT_OUTPUT_STREAM_INFO; only valid once an output type is set.
		var info [3]uint32
		d.mft.call3(mftGetOutputStreamInfo, 0, uintptr(unsafe.Pointer(&info)), 0)
		if info[0]&streamProvidesSamples == 0 {
			return errors.New("the transform does not provide output samples on the GPU")
		}
		d.format = capture.FormatNV12
		if sub == formatP010 {
			d.format = capture.FormatP010
		}
		return d.readSize()
	}
}

func (d *Decoder) readSize() error {
	var t obj
	if hr := d.mft.call3(mftGetOutputCurrentType, 0, uintptr(unsafe.Pointer(&t)), 0); hr.Failed() {
		return fmt.Errorf("GetOutputCurrentType: %w", hr)
	}
	defer t.release()
	if v, ok := getUINT64(t, &mtFrameSize); ok {
		d.width, d.height = int(v>>32), int(uint32(v))
	}
	// MFVideoArea: the visible part of the coded size.
	var area [4]int32
	var n uint32
	if !t.call(attrGetBlob, uintptr(unsafe.Pointer(&mtMinDisplayAperture)), uintptr(unsafe.Pointer(&area[0])), 16, uintptr(unsafe.Pointer(&n))).Failed() && n == 16 {
		d.width, d.height = int(area[2]), int(area[3])
	}
	return nil
}

// maxWait bounds how long Decode waits for the picture of a drained
// access unit.
const maxWait = 50 * time.Millisecond

// Decode copies au into a Media Foundation sample (one CPU copy of the
// compressed bytes), decodes it, and returns the newest picture ready: when
// a late picture and the current one are both ready, the late one is
// released unshown (counted by Dropped).
func (d *Decoder) Decode(au []byte, pts int64) (decode.Frame, error) {
	start := clock.Now()
	if err := d.input(au, pts); err != nil {
		return decode.Frame{}, err
	}
	var got decode.Frame
	have, drained := false, false
	for {
		f, err := d.output()
		switch {
		case errors.Is(err, errStreamChange):
			if err := d.chooseOutput(); err != nil {
				return decode.Frame{}, err
			}
			continue
		case errors.Is(err, decode.ErrNoFrame):
			if have {
				got.Latency = clock.Now() - start
				return got, nil
			}
			// Every Planck access unit carries one picture, but
			// AV1VideoExtension now and then keeps a decoded picture until
			// more input arrives (seen on the RX 6700 XT and RTX 4080).
			// Draining releases it and the stream continues intact.
			if !drained {
				d.mft.call3(mftProcessMessage, msgCommandDrain, 0, 0)
				drained = true
				continue
			}
			if clock.Now()-start > maxWait {
				return decode.Frame{}, decode.ErrNoFrame
			}
			runtime.Gosched()
			continue
		case err != nil:
			if have {
				d.Release(got)
			}
			return decode.Frame{}, err
		}
		if have {
			d.Release(got)
			d.dropped++
		}
		got, have = f, true
	}
}

// Dropped counts the pictures Decode released unshown because a newer one
// was ready with them.
func (d *Decoder) Dropped() int { return d.dropped }

var errStreamChange = errors.New("d3d11: output format changed")

func (d *Decoder) input(au []byte, pts int64) error {
	if len(au) == 0 {
		return errors.New("d3d11: empty access unit")
	}
	if r, _, _ := procMFCreateMemoryBuffer.Call(uintptr(len(au)), uintptr(unsafe.Pointer(d.buffer))); win.HRESULT(r).Failed() {
		return fmt.Errorf("d3d11: MFCreateMemoryBuffer: %w", win.HRESULT(r))
	}
	defer d.buffer.release()
	if hr := d.buffer.call3(bufLock, uintptr(unsafe.Pointer(d.locked)), 0, 0); hr.Failed() {
		return fmt.Errorf("d3d11: lock input: %w", hr)
	}
	copy(unsafe.Slice((*byte)(win.Ptr(*d.locked)), len(au)), au)
	d.buffer.call3(bufUnlock, 0, 0, 0)
	d.buffer.call3(bufSetCurrentLength, uintptr(len(au)), 0, 0)
	if r, _, _ := procMFCreateSample.Call(uintptr(unsafe.Pointer(d.sample))); win.HRESULT(r).Failed() {
		return fmt.Errorf("d3d11: MFCreateSample: %w", win.HRESULT(r))
	}
	defer d.sample.release()
	d.sample.call3(sampleAddBuffer, uintptr(*d.buffer), 0, 0)
	// Sample times are in 100 ns units; PTS passes through unchanged.
	d.sample.call3(sampleSetSampleTime, uintptr(pts), 0, 0)
	hr := d.mft.call3(mftProcessInput, 0, uintptr(*d.sample), 0)
	if hr == eNotAccepting {
		return errors.New("d3d11: decoder holds an undelivered picture; Release frames before decoding more")
	}
	if hr.Failed() {
		return fmt.Errorf("d3d11: ProcessInput: %w", hr)
	}
	return nil
}

func (d *Decoder) output() (decode.Frame, error) {
	*d.out = [4]uintptr{}
	*d.status = 0
	hr := d.mft.call3(mftProcessOutput, 0, 1, uintptr(unsafe.Pointer(d.out)))
	if ev := obj(d.out[3]); ev != 0 {
		ev.release()
	}
	switch {
	case hr == eNeedMoreInput:
		return decode.Frame{}, decode.ErrNoFrame
	case hr == eStreamChange || hr == eTypeNotSet:
		return decode.Frame{}, errStreamChange
	case hr.Failed():
		return decode.Frame{}, fmt.Errorf("d3d11: ProcessOutput: %w", hr)
	}
	sample := obj(d.out[1])
	if sample == 0 {
		return decode.Frame{}, decode.ErrNoFrame
	}
	if hr := sample.call3(sampleGetBufferByIndex, 0, uintptr(unsafe.Pointer(d.buffer)), 0); hr.Failed() {
		sample.release()
		return decode.Frame{}, fmt.Errorf("d3d11: output buffer: %w", hr)
	}
	defer d.buffer.release()
	if hr := d.buffer.call3(0, uintptr(unsafe.Pointer(&iidDXGIBuffer)), uintptr(unsafe.Pointer(d.dxgi)), 0); hr.Failed() {
		sample.release()
		return decode.Frame{}, fmt.Errorf("d3d11: output is not a DXGI buffer: %w", hr)
	}
	defer d.dxgi.release()
	d.dxgi.call3(dxgiBufGetSubresourceIndex, uintptr(unsafe.Pointer(d.slice)), 0, 0)
	if hr := d.dxgi.call3(dxgiBufGetResource, uintptr(unsafe.Pointer(&win.IID_ID3D11Texture2D)), uintptr(unsafe.Pointer(d.tex)), 0); hr.Failed() {
		sample.release()
		return decode.Frame{}, fmt.Errorf("d3d11: output texture: %w", hr)
	}
	// The sample keeps the texture alive; the frame holds the sample.
	tex := *d.tex
	d.tex.release()
	sample.call3(sampleGetSampleTime, uintptr(unsafe.Pointer(d.pts)), 0, 0)
	d.pending++
	return decode.Frame{Surface: uintptr(tex), Slice: int(*d.slice), Format: d.format, Width: d.width, Height: d.height,
		PTS: *d.pts, Ref: uintptr(sample)}, nil
}

// Release gives the picture's texture slice back to the decoder.
func (d *Decoder) Release(f decode.Frame) {
	if f.Ref != 0 {
		s := obj(f.Ref)
		s.release()
		d.pending--
	}
}

func (d *Decoder) Close() error {
	if d.mft != 0 {
		d.mft.call3(mftProcessMessage, msgCommandFlush, 0, 0)
		d.mft.release()
	}
	d.manager.release()
	return nil
}

var _ decode.Decoder = (*Decoder)(nil)
