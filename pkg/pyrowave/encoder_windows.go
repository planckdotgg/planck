//go:build windows

package pyrowave

import (
	"errors"
	"fmt"
	"unsafe"

	"github.com/planckdotgg/planck/pkg/capture"
	"github.com/planckdotgg/planck/pkg/encode"
	"github.com/planckdotgg/planck/pkg/gpu"
)

func init() {
	encode.RegisterCodec(encode.CodecBackend{Name: "PyroWave", Codec: encode.PyroWave,
		Open: func(dev gpu.Device, cfg encode.Config) (encode.Encoder, error) {
			e, err := OpenEncoder(dev, cfg)
			if err != nil {
				return nil, err
			}
			return e, nil
		}})
}

// Encoder is the Direct3D 11 compute encoder. Frames are read on the GPU
// that captured them; the only readback is the coded bitstream, which the
// CPU frames into chunks. Not safe for concurrent use.
type Encoder struct {
	d       *d3d
	plan    *plan
	cfg     encode.Config
	adapter gpu.Adapter
	packer  *Packer
	header  SequenceHeader

	coef, bands, stats, rc, out gpuBuf
	staging                     uintptr
	srcCopy                     uintptr
	srcCopyDesc                 texture2DDesc
	f                           *frameState
	budgetWords                 uint32
	kr, kb                      float32
	prof                        *profiler
}

// OpenEncoder opens the encoder on dev (an ID3D11Device*): the capture
// device, so input textures stay on their GPU.
func OpenEncoder(dev gpu.Device, cfg encode.Config) (*Encoder, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if cfg.Codec != encode.PyroWave {
		return nil, fmt.Errorf("%w: %s", encode.ErrUnsupported, cfg.Codec)
	}
	if dev.Handle == 0 {
		return nil, errors.New("pyrowave: no D3D11 device")
	}
	l, err := NewLayout(cfg.Width, cfg.Height, !cfg.Chroma.Full())
	if err != nil {
		return nil, err
	}
	e := &Encoder{plan: newPlan(l), cfg: cfg, adapter: dev.Adapter, f: &frameState{}}
	if e.packer, err = NewPacker(l, cfg.ChunkLen); err != nil {
		return nil, err
	}
	e.header = SequenceHeader{Width: cfg.Width, Height: cfg.Height, Chroma: ChromaRes420}
	if cfg.Chroma.Full() {
		e.header.Chroma = ChromaRes444
	}
	if cfg.Source == capture.FormatRGB10A2 {
		e.header.Primaries, e.header.Transfer, e.header.Matrix = PrimariesBT2020, TransferPQ, MatrixBT2020
	}
	e.kr, e.kb = matrixWeights(e.header.Matrix)
	if e.d, err = newD3D(dev.Handle); err != nil {
		return nil, err
	}
	if err := e.init(); err != nil {
		e.Close()
		return nil, err
	}
	e.SetBitrate(cfg.BitrateKbps)
	return e, nil
}

func (e *Encoder) init() error {
	src := shaderSource("hlsl/common.hlsl") + shaderSource("hlsl/encode.hlsl")
	if err := e.d.compile(src, "convert", "dwt", "analyze", "finalize", "pack"); err != nil {
		return err
	}
	l := e.plan.L
	table := make([]byte, 4*len(e.plan.table))
	for i, v := range e.plan.table {
		*(*uint32)(unsafe.Pointer(&table[4*i])) = v
	}
	var err error
	if e.coef, err = e.d.rawBuffer(4*int(e.plan.total), nil); err != nil {
		return err
	}
	if e.bands, err = e.d.rawBuffer(len(table), table); err != nil {
		return err
	}
	if e.stats, err = e.d.rawBuffer(4*statWords*l.TotalBlocks, nil); err != nil {
		return err
	}
	if e.rc, err = e.d.rawBuffer(4*rcWords, nil); err != nil {
		return err
	}
	if e.out, err = e.d.rawBuffer(outPayload+4*maxBlockWords*l.TotalBlocks, nil); err != nil {
		return err
	}
	e.staging, err = e.d.stagingBuffer(e.out.size)
	return err
}

func (e *Encoder) Caps() encode.Caps {
	chroma := []encode.Chroma{encode.NV12, encode.P010, encode.YUV444, encode.YUV444P10}
	return encode.Caps{Adapter: e.adapter, Codecs: []encode.Codec{encode.PyroWave},
		Chroma: map[encode.Codec][]encode.Chroma{encode.PyroWave: chroma}, MaxWidth: MaxDimension, MaxHeight: MaxDimension, Engines: 1}
}

// SetBitrate sets the next frame's budget: the bitrate's share of a frame
// less the chunk framing. FEC parity is the transport's, on top.
func (e *Encoder) SetBitrate(kbps int) error {
	if kbps <= 0 {
		return fmt.Errorf("pyrowave: bitrate %d", kbps)
	}
	bytes := float64(kbps) * 1000 / 8 / float64(e.cfg.FPS) * (1 - e.packer.Overhead())
	words := int(bytes/4) - 2
	limit := (e.out.size - outPayload) / 4
	e.budgetWords = uint32(min(max(words, 64), limit))
	return nil
}

// srv returns a shader view of frame's texture, copying it first into an
// owned texture when it cannot be bound as a shader resource.
func (e *Encoder) srv(frame capture.Frame) (uintptr, error) {
	f := e.f
	tex := frame.Surface
	call1(tex, texGetDesc, uintptr(unsafe.Pointer(&f.tex)))
	if f.tex.BindFlags&bindShaderResource == 0 {
		if e.srcCopy == 0 || e.srcCopyDesc.Width != f.tex.Width || e.srcCopyDesc.Height != f.tex.Height || e.srcCopyDesc.Format != f.tex.Format {
			release(&e.srcCopy)
			d := f.tex
			d.MipLevels, d.ArraySize, d.SampleCount, d.SampleQuality = 1, 1, 1, 0
			d.Usage, d.BindFlags, d.CPUAccessFlags, d.MiscFlags = usageDefault, bindShaderResource, 0, 0
			if err := hr(call(e.d.dev, devCreateTexture2D, uintptr(unsafe.Pointer(&d)), 0, uintptr(unsafe.Pointer(&e.srcCopy)))); err != nil {
				return 0, fmt.Errorf("pyrowave: input copy texture: %w", err)
			}
			e.srcCopyDesc = d
		}
		call2(e.d.ctx, ctxCopyResource, e.srcCopy, tex)
		tex = e.srcCopy
	}
	f.view = srvDesc{Format: f.tex.Format, ViewDimension: srvDimTexture2D, U0: 0, U1: 1}
	f.srvs[0] = 0
	if err := hr(call3(e.d.dev, devCreateShaderResourceView, tex, uintptr(unsafe.Pointer(&f.view)), uintptr(unsafe.Pointer(&f.srvs[0])))); err != nil {
		return 0, fmt.Errorf("pyrowave: input view (format %d): %w", f.tex.Format, err)
	}
	return f.srvs[0], nil
}

// Encode codes frame and appends its chunks to dst. Every packet is a
// keyframe; forceKey has nothing to do.
func (e *Encoder) Encode(frame capture.Frame, forceKey bool, dst []byte) (encode.Packet, error) {
	if frame.Width != e.cfg.Width || frame.Height != e.cfg.Height {
		return encode.Packet{}, fmt.Errorf("pyrowave: frame is %dx%d, encoder %dx%d", frame.Width, frame.Height, e.cfg.Width, e.cfg.Height)
	}
	f := e.f
	srv, err := e.srv(frame)
	if err != nil {
		return encode.Packet{}, err
	}
	d := e.d
	d.profBegin(e.prof)
	call2(d.ctx, ctxClearUnorderedAccessViewUint, e.rc.uav, uintptr(unsafe.Pointer(&f.clear[0])))
	f.uavs = [6]uintptr{e.coef.uav, e.bands.uav, e.stats.uav, e.rc.uav, e.out.uav, 0}
	d.bind(f, 5)
	f.srvs[0] = srv
	call3(d.ctx, ctxCSSetShaderResources, 0, 1, uintptr(unsafe.Pointer(&f.srvs[0])))

	pl := e.plan
	f.p = pl.baseParams()
	f.p.Seq = uint32(e.header.Sequence)
	f.p.BudgetWords = e.budgetWords
	f.p.Kr, f.p.Kb = e.kr, e.kb
	d.dispatch(f, "convert", (int(f.p.ChromaW)+15)/16, (int(f.p.ChromaH)+15)/16)
	d.mark(e.prof, "convert")
	for _, ps := range pl.passes {
		f.p.PlaneW, f.p.PlaneH = int32(ps.w), int32(ps.h)
		f.p.InOff, f.p.OutOff = ps.in, ps.out
		d.dispatch(f, "dwt", (ps.w+31)/32, (ps.h+31)/32)
	}
	d.mark(e.prof, "dwt")
	gx, gy := groups(pl.L.TotalBlocks)
	f.p.GroupsX = uint32(gx)
	d.dispatch(f, "analyze", gx, gy)
	d.mark(e.prof, "analyze")
	d.dispatch(f, "finalize", 1, 1)
	d.mark(e.prof, "finalize")
	d.dispatch(f, "pack", gx, gy)
	d.mark(e.prof, "pack")
	d.unbind(f)
	release(&srv)

	copyBytes := min(e.out.size, outPayload+4*int(e.budgetWords)+64)
	f.box = box{0, 0, 0, uint32(copyBytes), 1, 1}
	call8(d.ctx, ctxCopySubresourceRegion, e.staging, 0, 0, 0, 0, e.out.buf, 0, uintptr(unsafe.Pointer(&f.box)))
	d.mark(e.prof, "readback")
	d.wait(f)
	d.profEnd(e.prof)
	if err := hr(call5(d.ctx, ctxMap, e.staging, 0, mapRead, 0, uintptr(unsafe.Pointer(&f.m)))); err != nil {
		return encode.Packet{}, fmt.Errorf("pyrowave: map bitstream: %w", err)
	}
	defer call2(d.ctx, ctxUnmap, e.staging, 0)
	data := unsafe.Slice((*byte)(ptr(f.m.Data)), copyBytes)
	words := int(*(*uint32)(unsafe.Pointer(&data[0])))
	if outPayload+4*words > copyBytes {
		return encode.Packet{}, fmt.Errorf("pyrowave: %d words coded, budget %d", words, e.budgetWords)
	}
	pk, err := e.packer.Pack(dst, e.header, data[outPayload:outPayload+4*words])
	if err != nil {
		return encode.Packet{}, err
	}
	e.header.Sequence = (e.header.Sequence + 1) & 7
	return encode.Packet{Data: pk.Data, Keyframe: true, SliceEnds: pk.SliceEnds, Critical: pk.Critical, PTS: int64(frame.Presented)}, nil
}

func (e *Encoder) Close() error {
	if e.d == nil {
		return nil
	}
	for _, b := range []*gpuBuf{&e.coef, &e.bands, &e.stats, &e.rc, &e.out} {
		b.release()
	}
	release(&e.staging)
	release(&e.srcCopy)
	e.prof.release()
	e.d.close()
	e.d = nil
	return nil
}
