//go:build windows

package pyrowave

import (
	"errors"
	"fmt"
	"time"
	"unsafe"

	"github.com/planckdotgg/planck/internal/clock"

	"github.com/planckdotgg/planck/pkg/capture"
	"github.com/planckdotgg/planck/pkg/decode"
	"github.com/planckdotgg/planck/pkg/encode"
	"github.com/planckdotgg/planck/pkg/gpu"
)

func init() {
	decode.Register(decode.Backend{Name: "PyroWave D3D11", Codec: encode.PyroWave,
		Open: func(t decode.Target, cfg decode.Config) (decode.Decoder, error) {
			d, err := OpenD3D11Decoder(gpu.Device{Adapter: t.Adapter, Handle: t.Device}, cfg)
			if err != nil {
				return nil, err
			}
			return d, nil
		}})
}

// D3D11Decoder decodes on the presenting surface's device into a pool of
// textures the surface presents directly. Bytes only go CPU to GPU.
// Decode does not wait for the GPU; the context's order puts the present
// after it. Finish waits and measures.
type D3D11Decoder struct {
	d      *d3d
	plan   *plan
	cfg    decode.Config
	parser *Parser
	upload []byte

	coef, bands, recs, up gpuBuf
	out                   [outputPool]struct {
		tex, uav uintptr
		start    time.Duration
	}
	next   int
	format capture.Format
	f      *frameState
	prof   *profiler
}

// OpenD3D11Decoder opens a decoder on dev (an ID3D11Device*).
func OpenD3D11Decoder(dev gpu.Device, cfg decode.Config) (*D3D11Decoder, error) {
	if dev.Handle == 0 {
		return nil, errors.New("pyrowave: no D3D11 device")
	}
	if cfg.ChunkLen < MinChunkLen {
		return nil, fmt.Errorf("pyrowave: chunk length %d below %d", cfg.ChunkLen, MinChunkLen)
	}
	l, err := NewLayout(cfg.Width, cfg.Height, !cfg.Chroma.Full())
	if err != nil {
		return nil, err
	}
	dd := &D3D11Decoder{plan: newPlan(l), cfg: cfg, parser: NewParser(l, cfg.ChunkLen), f: &frameState{}, format: capture.FormatBGRA8}
	if cfg.Chroma.TenBit() {
		dd.format = capture.FormatRGB10A2
	}
	if dd.d, err = newD3D(dev.Handle); err != nil {
		return nil, err
	}
	if err := dd.init(); err != nil {
		dd.Close()
		return nil, err
	}
	return dd, nil
}

func (dd *D3D11Decoder) init() error {
	src := shaderSource("hlsl/common.hlsl") + shaderSource("hlsl/decode.hlsl")
	if err := dd.d.compile(src, "clear", "dequant", "idwt", "yuv2rgb"); err != nil {
		return err
	}
	l := dd.plan.L
	table := make([]byte, 4*len(dd.plan.table))
	for i, v := range dd.plan.table {
		*(*uint32)(unsafe.Pointer(&table[4*i])) = v
	}
	maxFrame := maxSlices * maxShards * dd.cfg.ChunkLen
	dd.upload = make([]byte, maxFrame)
	var err error
	if dd.coef, err = dd.d.rawBuffer(4*int(dd.plan.total), nil); err != nil {
		return err
	}
	if dd.bands, err = dd.d.rawBuffer(len(table), table); err != nil {
		return err
	}
	if dd.recs, err = dd.d.rawBuffer(16*cap(dd.parser.Recs), nil); err != nil {
		return err
	}
	if dd.up, err = dd.d.rawBuffer(maxFrame, nil); err != nil {
		return err
	}
	format := uint32(formatB8G8R8A8Unorm)
	if dd.format == capture.FormatRGB10A2 {
		format = formatR10G10B10A2Unorm
	}
	for i := range dd.out {
		desc := texture2DDesc{Width: uint32(l.Width), Height: uint32(l.Height), MipLevels: 1, ArraySize: 1, Format: format,
			SampleCount: 1, Usage: usageDefault, BindFlags: bindShaderResource | bindUnorderedAccess}
		o := &dd.out[i]
		if err := hr(call(dd.d.dev, devCreateTexture2D, uintptr(unsafe.Pointer(&desc)), 0, uintptr(unsafe.Pointer(&o.tex)))); err != nil {
			return fmt.Errorf("pyrowave: output texture: %w", err)
		}
		ud := uavDesc{Format: format, ViewDimension: uavDimTexture2D}
		if err := hr(call(dd.d.dev, devCreateUnorderedAccessView, o.tex, uintptr(unsafe.Pointer(&ud)), uintptr(unsafe.Pointer(&o.uav)))); err != nil {
			return fmt.Errorf("pyrowave: output UAV (format %d): %w", format, err)
		}
	}
	return nil
}

// Decode decodes one chunked frame (Reassembler.PartialFrame's output) and
// returns its texture. Lost chunks decode as zero coefficients.
func (dd *D3D11Decoder) Decode(au []byte, pts int64) (decode.Frame, error) {
	start := clock.Now()
	if len(au) > len(dd.upload) {
		return decode.Frame{}, fmt.Errorf("pyrowave: %d-byte frame over the %d-byte limit", len(au), len(dd.upload))
	}
	hdr, used, err := dd.parser.Parse(au, dd.upload)
	if err != nil {
		return decode.Frame{}, err
	}
	d, f, pl := dd.d, dd.f, dd.plan
	nrec := len(dd.parser.Recs)
	d.profBegin(dd.prof)
	if used > 0 {
		f.box = box{0, 0, 0, uint32(used), 1, 1}
		call6(d.ctx, ctxUpdateSubresource, dd.up.buf, 0, uintptr(unsafe.Pointer(&f.box)), uintptr(unsafe.Pointer(&dd.upload[0])), 0, 0)
		f.box = box{0, 0, 0, uint32(16 * nrec), 1, 1}
		call6(d.ctx, ctxUpdateSubresource, dd.recs.buf, 0, uintptr(unsafe.Pointer(&f.box)), uintptr(unsafe.Pointer(&dd.parser.Recs[0])), 0, 0)
	}
	d.mark(dd.prof, "upload")
	o := &dd.out[dd.next]
	o.start = start
	dd.next = (dd.next + 1) % outputPool
	f.uavs = [6]uintptr{dd.coef.uav, dd.bands.uav, dd.recs.uav, dd.up.uav, o.uav, 0}
	f.srvs[0] = 0
	d.bind(f, 5)

	f.p = pl.baseParams()
	f.p.Kr, f.p.Kb = matrixWeights(hdr.Matrix)
	n := (int(pl.coded) + 3) / 4
	gx, gy := groups((n + 255) / 256)
	f.p.GroupsX, f.p.OutOff[0] = uint32(gx), pl.coded
	d.dispatch(f, "clear", gx, gy)
	d.mark(dd.prof, "clear")
	if nrec > 0 {
		gx, gy = groups(nrec)
		f.p.GroupsX, f.p.TotalBlocks = uint32(gx), uint32(nrec)
		d.dispatch(f, "dequant", gx, gy)
	}
	d.mark(dd.prof, "dequant")
	for i := len(pl.passes) - 1; i >= 0; i-- {
		ps := pl.passes[i]
		f.p.PlaneW, f.p.PlaneH = int32(ps.w), int32(ps.h)
		f.p.InOff, f.p.OutOff = ps.in, ps.out
		d.dispatch(f, "idwt", (ps.w+31)/32, (ps.h+31)/32)
	}
	d.mark(dd.prof, "idwt")
	f.p.PlaneW, f.p.PlaneH = int32(pl.L.AlignedWidth), int32(pl.L.AlignedHeight)
	f.p.OutOff = [4]uint32{pl.planeOff[0], pl.planeOff[1], pl.planeOff[2], 0}
	d.dispatch(f, "yuv2rgb", (pl.L.Width+15)/16, (pl.L.Height+15)/16)
	d.mark(dd.prof, "yuv2rgb")
	d.unbind(f)
	call0(d.ctx, ctxFlush)
	return decode.Frame{Surface: o.tex, Format: dd.format, Width: pl.L.Width, Height: pl.L.Height, PTS: pts}, nil
}

// Wait blocks until the GPU has finished every decode submitted.
func (dd *D3D11Decoder) Wait() {
	dd.d.wait(dd.f)
	dd.d.profEnd(dd.prof)
}

// Finish waits until the GPU has finished every decode submitted, f's
// among them, and sets f.Latency.
func (dd *D3D11Decoder) Finish(f *decode.Frame) {
	dd.d.wait(dd.f)
	for i := range dd.out {
		if dd.out[i].tex == f.Surface {
			f.Latency = clock.Now() - dd.out[i].start
			return
		}
	}
}

// Release is a no-op: output textures are reused round robin, three deep.
func (dd *D3D11Decoder) Release(decode.Frame) {}

func (dd *D3D11Decoder) Close() error {
	if dd.d == nil {
		return nil
	}
	for i := range dd.out {
		release(&dd.out[i].uav)
		release(&dd.out[i].tex)
	}
	for _, b := range []*gpuBuf{&dd.coef, &dd.bands, &dd.recs, &dd.up} {
		b.release()
	}
	dd.prof.release()
	dd.d.close()
	dd.d = nil
	return nil
}
