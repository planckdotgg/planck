//go:build linux && !android && (amd64 || arm64)

package vaapi

import (
	"bytes"
	"fmt"
	"syscall"
	"unsafe"

	"github.com/planckdotgg/planck/pkg/capture"
	"github.com/planckdotgg/planck/pkg/codec/hevc"
	"github.com/planckdotgg/planck/pkg/encode"
	"github.com/planckdotgg/planck/pkg/gpu"
)

// Sizes and offsets of libva 2.20's HEVC encode structs on LP64, from
// offsetof in a C program built against va_enc_hevc.h.
const (
	sizePictureHEVC = 28

	sizeSeq     = 116
	seqProfile  = 0
	seqLevel    = 1
	seqIntra    = 4
	seqIntraIDR = 8
	seqIPPeriod = 12
	seqBitrate  = 16
	seqWidth    = 20
	seqHeight   = 22
	seqFields   = 24
	seqMinCb    = 28
	seqDiffCb   = 29
	seqMinTb    = 30
	seqDiffTb   = 31
	seqTHInter  = 32
	seqTHIntra  = 33

	sizePic      = 576
	picCurr      = 0
	picRefs      = 28
	picCodedBuf  = 448
	picColocated = 452
	picInitQP    = 454
	picL0Default = 502
	picNalType   = 505
	picFields    = 508

	sizeSlice      = 1076
	sliceAddress   = 0
	sliceNumCtu    = 4
	sliceType      = 8
	sliceList0     = 12
	sliceList1     = 432
	sliceMergeCand = 1034
	sliceQPDelta   = 1035
	sliceFields    = 1040

	sizeMiscRC        = 60
	rcBitrate         = 0
	rcTargetPercent   = 4
	rcWindow          = 8
	rcFlags           = 24
	rcFlagReset       = 1
	rcFlagNoFrameSkip = 2
	rcFlagNoStuffing  = 4
	sizeMiscFrameRate = 24
	sizeMiscHRD       = 24
	hrdFullness       = 0
	hrdSize           = 4

	sizePackedParam = 28
	packedType      = 0
	packedBits      = 4
	packedEmulation = 8

	segSize = 0
	segBuf  = 16
	segNext = 24

	sizePipeline      = 224
	ppSurface         = 0
	ppSurfaceRegion   = 8
	ppSurfaceStandard = 16
	ppOutputRegion    = 24
	ppBackground      = 32
	ppOutputStandard  = 36
	ppInputProps      = 132
	ppOutputProps     = 140
	colorPropsRange   = 1
)

// HEVC feature support (VAConfigAttribValEncHEVCFeatures): two bits each.
const (
	featAMP         = 4
	featSAO         = 6
	featTemporalMVP = 10
	featStrongIntra = 12
	featSignHiding  = 16
	featCIP         = 18
	featTSkip       = 20
	featCuQPDelta   = 22
	featRequired    = 2
)

type rect struct {
	x, y int16
	w, h uint16
}

type importKey struct {
	serial uint64
	fd     int32
}

// Encoder is a VA-API HEVC encoder of one stream.
type Encoder struct {
	d       *display
	adapter gpu.Adapter
	cfg     encode.Config
	caps    encode.Caps

	config, ctx uint32
	// made records what exists to destroy: VA IDs may be 0.
	made       struct{ config, ctx, surfaces, coded, rgb bool }
	rt, fourcc uint32
	w, h       int // coded size, a multiple of the minimum coding block
	input      uint32
	recon      [2]uint32
	coded      uint32
	cur        int
	haveRef    bool
	poc        uint32
	frame      int64

	stream    hevc.Stream
	packed    bool
	lowDelayB bool
	ctbs      int
	slices    []uint32 // first CTB of each slice, and the end
	sendRC    bool
	paramSets []byte

	vpp, vppCtx  uint32
	haveVPP      bool
	rgb          uint32
	rgbFourcc    uint32
	imports      map[importKey]uint32
	inRect, outR rect

	seq, pic, slice, pipeline []byte
	bufs                      []uint32
}

// Open starts an HEVC encoder on adapter's render node.
func Open(adapter gpu.Adapter, cfg encode.Config) (*Encoder, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	switch {
	case cfg.Codec != encode.HEVC:
		return nil, fmt.Errorf("%w: %s on VA-API (HEVC only)", encode.ErrUnsupported, cfg.Codec)
	case cfg.Chroma.Full():
		return nil, fmt.Errorf("%w: 4:4:4 on VA-API", encode.ErrUnsupported)
	case cfg.SubFrame:
		return nil, fmt.Errorf("%w: sub-frame output on VA-API", encode.ErrUnsupported)
	case cfg.HDR10:
		return nil, fmt.Errorf("%w: HDR10 on VA-API", encode.ErrUnsupported)
	case cfg.Source == capture.FormatRGB10A2 || cfg.Source == capture.FormatRGBA16F:
		return nil, fmt.Errorf("%w: %s input on VA-API", encode.ErrUnsupported, cfg.Source)
	}
	d, err := openDisplay(adapter)
	if err != nil {
		return nil, err
	}
	e := &Encoder{d: d, adapter: adapter, cfg: cfg, imports: map[importKey]uint32{},
		seq: make([]byte, sizeSeq), pic: make([]byte, sizePic), slice: make([]byte, sizeSlice), pipeline: make([]byte, sizePipeline)}
	if e.adapter.Name == "" {
		e.adapter.Name = d.vendor
	}
	if err := e.init(); err != nil {
		e.Close()
		return nil, err
	}
	return e, nil
}

func (e *Encoder) init() error {
	cfg := e.cfg
	profile, rt, fourcc, depth := uintptr(vaProfileHEVCMain), uint32(vaRTFormatYUV420), uint32(vaFourccNV12), uint32(0)
	if cfg.Chroma.TenBit() {
		profile, rt, fourcc, depth = vaProfileHEVCMain10, vaRTFormatYUV420_10, vaFourccP010, 2
	}
	eps := e.d.entrypoints(profile)
	entry := uintptr(vaEntrypointEncSliceLP)
	if !eps[vaEntrypointEncSliceLP] {
		entry = vaEntrypointEncSlice
		if !eps[vaEntrypointEncSlice] {
			return fmt.Errorf("%w: %s %s encode on %s", encode.ErrUnsupported, cfg.Codec, cfg.Chroma, e.adapter.Name)
		}
	}
	a := e.d.attribs(profile, entry, vaConfigAttribRTFormat, vaConfigAttribRateControl, vaConfigAttribEncPackedHeaders,
		vaConfigAttribEncMaxSlices, vaConfigAttribPredictionDirection, vaConfigAttribEncHEVCFeatures, vaConfigAttribEncHEVCBlockSizes)
	if a[vaConfigAttribRTFormat]&rt == 0 {
		return fmt.Errorf("%w: render target format %#x", encode.ErrUnsupported, rt)
	}
	rc := uint32(vaRCCBR)
	if a[vaConfigAttribRateControl]&vaRCCBR == 0 {
		if rc = vaRCVBR; a[vaConfigAttribRateControl]&vaRCVBR == 0 {
			return fmt.Errorf("%w: neither CBR nor VBR rate control", encode.ErrUnsupported)
		}
	}
	attrs := []configAttrib{{vaConfigAttribRTFormat, rt}, {vaConfigAttribRateControl, rc}}
	if p := a[vaConfigAttribEncPackedHeaders]; p != vaAttribNotSupported {
		// Both, or none: the driver's slice headers would follow its own
		// parameter sets, not ours.
		e.packed = p&(vaPackedHeaderSequence|vaPackedHeaderSlice) == vaPackedHeaderSequence|vaPackedHeaderSlice
		v := uint32(0)
		if e.packed {
			v = vaPackedHeaderSequence | vaPackedHeaderSlice
		}
		attrs = append(attrs, configAttrib{vaConfigAttribEncPackedHeaders, v})
	}
	if p := a[vaConfigAttribPredictionDirection]; p != vaAttribNotSupported {
		e.lowDelayB = p&vaPredictionBiNotEmpty != 0
	}
	var err error
	if e.config, err = e.d.createConfig(profile, entry, attrs); err != nil {
		return err
	}
	e.made.config = true
	e.rt, e.fourcc = rt, fourcc

	e.stream = streamFor(cfg, depth, a[vaConfigAttribEncHEVCFeatures], a[vaConfigAttribEncHEVCBlockSizes])
	e.w, e.h = int(e.stream.Width), int(e.stream.Height)
	ctb := 1 << (e.stream.Log2MinCbMinus3 + 3 + e.stream.Log2DiffMaxMinCb)
	ctbW, ctbH := (e.w+ctb-1)/ctb, (e.h+ctb-1)/ctb
	e.ctbs = ctbW * ctbH
	n := max(cfg.Slices, 1)
	if m := a[vaConfigAttribEncMaxSlices]; m != vaAttribNotSupported && m > 0 {
		n = min(n, int(m))
	}
	n = min(n, ctbH)
	for i := range n {
		e.slices = append(e.slices, uint32(i*ctbH/n*ctbW))
	}
	e.slices = append(e.slices, uint32(e.ctbs))

	surfaces, err := e.d.createSurfaces(rt, fourcc, e.w, e.h, 3)
	if err != nil {
		return err
	}
	e.input, e.recon, e.made.surfaces = surfaces[0], [2]uint32{surfaces[1], surfaces[2]}, true
	if e.ctx, err = e.d.createContext(e.config, e.w, e.h, e.recon[:]); err != nil {
		return err
	}
	e.made.ctx = true
	coded := new(uint32)
	if err := status("vaCreateBuffer (coded)", call(lib.createBuffer, e.d.dpy, uintptr(e.ctx), vaEncCodedBufferType,
		uintptr(max(e.w*e.h*3/2, 1<<20)), 1, 0, uintptr(unsafe.Pointer(coded)))); err != nil {
		return err
	}
	e.coded, e.made.coded = *coded, true
	chroma := []encode.Chroma{encode.NV12}
	if cfg.Chroma.TenBit() {
		chroma = append(chroma, encode.P010)
	}
	e.caps = encode.Caps{Adapter: e.adapter, Codecs: []encode.Codec{encode.HEVC}, Chroma: map[encode.Codec][]encode.Chroma{encode.HEVC: chroma},
		MaxWidth: 4096, MaxHeight: 4096, Engines: 1}
	e.sendRC = true
	return nil
}

// streamFor fixes the stream's parameters from what the driver says it
// supports and requires: features and block sizes are VA-API's HEVC
// attributes, vaAttribNotSupported where the driver gives none.
func streamFor(cfg encode.Config, depth, features, blocks uint32) hevc.Stream {
	s := hevc.Stream{
		ProfileIdc: 1, LevelIdc: level(cfg.Width, cfg.Height, cfg.FPS), BitDepthMinus8: depth, Log2MaxPocLsb: 16,
		VisibleWidth: uint32(cfg.Width), VisibleHeight: uint32(cfg.Height), LoopFilterAcrossSlices: true,
		MaxDecPicBufferingMinus1: 1, InitQPMinus26: 4,
	}
	if depth > 0 {
		s.ProfileIdc = 2
	}
	// FFmpeg's defaults where the driver says nothing: 32x32 CTBs, 16x16
	// minimum coding blocks, transform blocks 4x4 to 32x32.
	minCb, ctb := uint32(4), uint32(5)
	s.Log2MinTbMinus2, s.Log2DiffMaxMinTb, s.MaxTHDepthInter, s.MaxTHDepthIntra = 0, 3, 3, 3
	if blocks != vaAttribNotSupported {
		ctb = blocks&3 + 3
		minCb = blocks>>4&3 + 3
		maxTb, minTb := blocks>>6&3+2, blocks>>8&3+2
		s.Log2MinTbMinus2, s.Log2DiffMaxMinTb = minTb-2, maxTb-minTb
		s.MaxTHDepthInter, s.MaxTHDepthIntra = blocks>>10&3, blocks>>14&3
	}
	s.Log2MinCbMinus3, s.Log2DiffMaxMinCb = minCb-3, ctb-minCb
	align := uint32(1) << minCb
	s.Width, s.Height = (s.VisibleWidth+align-1)&^(align-1), (s.VisibleHeight+align-1)&^(align-1)
	f := features
	if f == vaAttribNotSupported {
		f = 0
	}
	required := func(shift uint) bool { return f>>shift&3 == featRequired }
	s.AMP, s.SAO, s.TemporalMVP, s.StrongIntraSmoothing = required(featAMP), required(featSAO), required(featTemporalMVP), required(featStrongIntra)
	s.SignDataHiding, s.ConstrainedIntraPred, s.TransformSkip = required(featSignHiding), required(featCIP), required(featTSkip)
	s.CuQPDelta = features == vaAttribNotSupported || f>>featCuQPDelta&3 != 0
	return s
}

// level is the lowest HEVC level, times 30, whose picture size and luma
// sample rate hold w x h at fps (Table A.8).
func level(w, h, fps int) uint32 {
	ps, sr := int64(w)*int64(h), int64(w)*int64(h)*int64(fps)
	for _, l := range []struct {
		idc    uint32
		ps, sr int64
	}{
		{93, 983040, 33177600}, {120, 2228224, 66846720}, {123, 2228224, 133693440},
		{150, 8912896, 267386880}, {153, 8912896, 534773760}, {156, 8912896, 1069547520},
		{180, 35651584, 1069547520}, {183, 35651584, 2139095040}, {186, 35651584, 4278190080},
	} {
		if ps <= l.ps && sr <= l.sr {
			return l.idc
		}
	}
	return 186
}

func (e *Encoder) Caps() encode.Caps { return e.caps }

func putPicture(b []byte, off int, surface uint32, poc int32, flags uint32) {
	put32(b, off, surface)
	put32(b, off+4, uint32(poc))
	put32(b, off+8, flags)
}

func clearPictures(b []byte, off, n int) {
	for i := range n {
		putPicture(b, off+i*sizePictureHEVC, vaInvalidID, 0, vaPictureHEVCInvalid)
	}
}

func (e *Encoder) Encode(f capture.Frame, forceKey bool, dst []byte) (encode.Packet, error) {
	src, err := e.source(f)
	if err != nil {
		return encode.Packet{}, err
	}
	key := forceKey || !e.haveRef
	if key {
		e.poc = 0
	} else {
		e.poc++
	}
	if err := e.submit(src, key); err != nil {
		e.destroyBuffers()
		return encode.Packet{}, err
	}
	e.destroyBuffers()
	if err := status("vaSyncSurface", call(lib.syncSurface, e.d.dpy, uintptr(src))); err != nil {
		return encode.Packet{}, err
	}
	out, err := e.readCoded(dst[:0])
	if err != nil {
		return encode.Packet{}, err
	}
	if key {
		out = e.withParameterSets(out)
	}
	e.cur, e.haveRef = 1-e.cur, true
	pkt := encode.Packet{Data: out, Keyframe: key, PTS: e.frame}
	e.frame++
	return pkt, nil
}

// add makes a VA buffer for this picture.
func (e *Encoder) add(typ uint32, data []byte) error {
	id, err := e.d.buffer(e.ctx, typ, data)
	if err != nil {
		return err
	}
	e.bufs = append(e.bufs, id)
	return nil
}

func (e *Encoder) destroyBuffers() {
	for _, b := range e.bufs {
		call(lib.destroyBuffer, e.d.dpy, uintptr(b))
	}
	e.bufs = e.bufs[:0]
}

// bit is 1<<n when on.
func bit(n uint, on bool) uint32 {
	if on {
		return 1 << n
	}
	return 0
}

func misc(typ uint32, size int) []byte {
	b := make([]byte, 4+size)
	put32(b, 0, typ)
	return b
}

// submit renders the picture's buffers in FFmpeg's order: sequence and
// rate control, picture, packed parameter sets, then each slice's packed
// header and parameters.
func (e *Encoder) submit(src uint32, key bool) error {
	cfg, s := e.cfg, &e.stream
	bitrate := uint32(cfg.BitrateKbps) * 1000
	if key {
		q := e.seq
		clear(q)
		put8(q, seqProfile, uint8(s.ProfileIdc))
		put8(q, seqLevel, uint8(s.LevelIdc))
		// The client asks for keyframes; the periods only shape the
		// driver's rate control, which models a ten-second GOP.
		put32(q, seqIntra, uint32(cfg.FPS*10))
		put32(q, seqIntraIDR, uint32(cfg.FPS*10))
		put32(q, seqIPPeriod, 1)
		put32(q, seqBitrate, bitrate)
		put16(q, seqWidth, uint16(e.w))
		put16(q, seqHeight, uint16(e.h))
		fields := uint32(1) | s.BitDepthMinus8<<3 | s.BitDepthMinus8<<6 | 1<<16
		fields |= bit(10, s.StrongIntraSmoothing) | bit(11, s.AMP) | bit(12, s.SAO) | bit(15, s.TemporalMVP)
		put32(q, seqFields, fields)
		put8(q, seqMinCb, uint8(s.Log2MinCbMinus3))
		put8(q, seqDiffCb, uint8(s.Log2DiffMaxMinCb))
		put8(q, seqMinTb, uint8(s.Log2MinTbMinus2))
		put8(q, seqDiffTb, uint8(s.Log2DiffMaxMinTb))
		put8(q, seqTHInter, uint8(s.MaxTHDepthInter))
		put8(q, seqTHIntra, uint8(s.MaxTHDepthIntra))
		if err := e.add(vaEncSequenceParameterBufferType, q); err != nil {
			return err
		}
	}
	if key || e.sendRC {
		rc := misc(vaMiscRateControl, sizeMiscRC)
		put32(rc[4:], rcBitrate, bitrate)
		put32(rc[4:], rcTargetPercent, 100)
		put32(rc[4:], rcWindow, 1000)
		flags := uint32(rcFlagNoFrameSkip | rcFlagNoStuffing)
		if e.sendRC && e.haveRef {
			flags |= rcFlagReset
		}
		put32(rc[4:], rcFlags, flags)
		fr := misc(vaMiscFrameRate, sizeMiscFrameRate)
		put32(fr[4:], 0, uint32(cfg.FPS))
		// One frame of VBV, as NVENC runs: a picture leaves at the rate.
		hrd := misc(vaMiscHRD, sizeMiscHRD)
		put32(hrd[4:], hrdFullness, bitrate/uint32(cfg.FPS))
		put32(hrd[4:], hrdSize, bitrate/uint32(cfg.FPS))
		for _, b := range [][]byte{rc, fr, hrd} {
			if err := e.add(vaEncMiscParameterBufferType, b); err != nil {
				return err
			}
		}
		e.sendRC = false
	}
	cur, ref := e.recon[e.cur], e.recon[1-e.cur]
	codingType, slice := uint32(1), hevc.SliceI
	if !key {
		codingType, slice = 2, hevc.SliceP
		if e.lowDelayB {
			codingType, slice = 3, hevc.SliceB
		}
	}
	p := e.pic
	clear(p)
	putPicture(p, picCurr, cur, int32(e.poc), 0)
	clearPictures(p, picRefs, 15)
	if !key {
		putPicture(p, picRefs, ref, int32(e.poc-1), 0)
	}
	put32(p, picCodedBuf, e.coded)
	put8(p, picColocated, 0xff)
	if s.TemporalMVP && !key {
		put8(p, picColocated, 0)
	}
	put8(p, picInitQP, uint8(26+s.InitQPMinus26))
	put8(p, picL0Default, 0)
	nalType := uint8(1)
	if key {
		nalType = hevc.NalIDRWRADL
	}
	put8(p, picNalType, nalType)
	fields := bit(0, key) | codingType<<1 | 1<<4 | bit(6, s.SignDataHiding) | bit(7, s.ConstrainedIntraPred) |
		bit(8, s.TransformSkip) | bit(9, s.CuQPDelta) | bit(16, s.LoopFilterAcrossSlices)
	put32(p, picFields, fields)
	if err := e.add(vaEncPictureParameterBufferType, p); err != nil {
		return err
	}
	if e.packed && key {
		var ps []byte
		for _, n := range [][]byte{s.VPS(), s.SPS(), s.PPS()} {
			ps = append(append(ps, 0, 0, 0, 1), n...)
		}
		if err := e.addPacked(vaPackedTypeSequence, ps); err != nil {
			return err
		}
	}
	for i := 0; i < len(e.slices)-1; i++ {
		h := hevc.Slice{IDR: key, Type: slice, Address: e.slices[i], PocLsb: e.poc & 0xffff, SAOLuma: s.SAO, SAOChroma: s.SAO, TemporalMVP: s.TemporalMVP}
		if e.packed {
			if err := e.addPacked(vaPackedTypeSlice, append([]byte{0, 0, 0, 1}, s.SliceHeader(h)...)); err != nil {
				return err
			}
		}
		b := e.slice
		clear(b)
		put32(b, sliceAddress, e.slices[i])
		put32(b, sliceNumCtu, e.slices[i+1]-e.slices[i])
		put8(b, sliceType, uint8(slice))
		clearPictures(b, sliceList0, 15)
		clearPictures(b, sliceList1, 15)
		if !key {
			putPicture(b, sliceList0, ref, int32(e.poc-1), 0)
			if e.lowDelayB {
				putPicture(b, sliceList1, ref, int32(e.poc-1), 0)
			}
		}
		put8(b, sliceMergeCand, uint8(5-s.FiveMinusMaxMergeCand))
		sf := bit(0, i == len(e.slices)-2) | bit(4, s.TemporalMVP && !key) | bit(5, s.SAO) | bit(6, s.SAO) |
			bit(12, s.LoopFilterAcrossSlices) | bit(13, e.lowDelayB && !key)
		put32(b, sliceFields, sf)
		if err := e.add(vaEncSliceParameterBufferType, b); err != nil {
			return err
		}
	}
	if err := status("vaBeginPicture", call(lib.beginPicture, e.d.dpy, uintptr(e.ctx), uintptr(src))); err != nil {
		return err
	}
	if err := status("vaRenderPicture", call(lib.renderPicture, e.d.dpy, uintptr(e.ctx), uintptr(unsafe.Pointer(&e.bufs[0])), uintptr(len(e.bufs)))); err != nil {
		call(lib.endPicture, e.d.dpy, uintptr(e.ctx))
		return err
	}
	return status("vaEndPicture", call(lib.endPicture, e.d.dpy, uintptr(e.ctx)))
}

// addPacked adds a packed header: its parameters, then its bytes, which
// carry their emulation prevention.
func (e *Encoder) addPacked(typ uint32, data []byte) error {
	p := make([]byte, sizePackedParam)
	put32(p, packedType, typ)
	put32(p, packedBits, uint32(len(data)*8))
	put8(p, packedEmulation, 1)
	if err := e.add(vaEncPackedHeaderParameterBufferType, p); err != nil {
		return err
	}
	return e.add(vaEncPackedHeaderDataBufferType, data)
}

// readCoded appends the coded buffer's segments to out.
func (e *Encoder) readCoded(out []byte) ([]byte, error) {
	seg := new(uintptr)
	if err := status("vaMapBuffer (coded)", call(lib.mapBuffer, e.d.dpy, uintptr(e.coded), uintptr(unsafe.Pointer(seg)))); err != nil {
		return nil, err
	}
	for s := *seg; s != 0; s = *(*uintptr)(ptr(s + segNext)) {
		n := *(*uint32)(ptr(s + segSize))
		if buf := *(*uintptr)(ptr(s + segBuf)); n > 0 && buf != 0 {
			out = append(out, unsafe.Slice((*byte)(ptr(buf)), n)...)
		}
	}
	return out, status("vaUnmapBuffer (coded)", call(lib.unmapBuffer, e.d.dpy, uintptr(e.coded)))
}

// withParameterSets keeps the parameter sets of a keyframe that carries
// them, and puts the last ones in front of a keyframe that does not, so
// every keyframe starts a decodable stream as with the other backends.
func (e *Encoder) withParameterSets(au []byte) []byte {
	var ps []byte
	for _, n := range hevc.NALUnits(au) {
		if t := hevc.NALType(n); t == hevc.NalVPS || t == hevc.NalSPS || t == hevc.NalPPS {
			ps = append(append(ps, 0, 0, 0, 1), n...)
		}
	}
	if len(ps) > 0 {
		e.paramSets = ps
		return au
	}
	if len(e.paramSets) == 0 {
		return au
	}
	return append(bytes.Clone(e.paramSets), au...)
}

// source is the VA surface holding the frame as the encoder's 4:2:0:
// planes in memory are uploaded, RGB is converted by the video processor,
// and a DMA-BUF is imported and copied on the GPU.
func (e *Encoder) source(f capture.Frame) (uint32, error) {
	d := capture.DMABufOf(f.Surface)
	if d == nil {
		return 0, fmt.Errorf("vaapi: frame has no picture")
	}
	if f.Width > e.w || f.Height > e.h {
		return 0, fmt.Errorf("vaapi: %dx%d frame for a %dx%d encoder", f.Width, f.Height, e.w, e.h)
	}
	vaFourcc, rgb, err := vaFormat(d.Fourcc)
	if err != nil {
		return 0, err
	}
	if !rgb && vaFourcc != e.fourcc {
		return 0, fmt.Errorf("%w: %#x frames into a %#x encoder", encode.ErrUnsupported, vaFourcc, e.fourcc)
	}
	if d.HasFDs() {
		in, err := e.importFrame(d, vaFourcc, rgb)
		if err != nil {
			return 0, err
		}
		return e.input, e.process(in, f.Width, f.Height, rgb)
	}
	if !d.HasCPU() {
		return 0, fmt.Errorf("vaapi: frame has neither memory nor DMA-BUF planes")
	}
	if !rgb {
		bpp := 1
		if vaFourcc == vaFourccP010 {
			bpp = 2
		}
		return e.input, e.d.upload(e.input, d.CPU, d.Pitches, f.Width*bpp, []int{f.Height, f.Height / 2})
	}
	if !e.made.rgb || e.rgbFourcc != vaFourcc {
		if e.made.rgb {
			e.d.destroySurfaces(e.rgb)
			e.made.rgb = false
		}
		s, err := e.d.createSurfaces(vaRTFormatRGB32, vaFourcc, e.w, e.h, 1)
		if err != nil {
			return 0, err
		}
		e.rgb, e.rgbFourcc, e.made.rgb = s[0], vaFourcc, true
	}
	if err := e.d.upload(e.rgb, d.CPU, d.Pitches, f.Width*4, []int{f.Height}); err != nil {
		return 0, err
	}
	return e.input, e.process(e.rgb, f.Width, f.Height, true)
}

func vaFormat(drm uint32) (fourcc uint32, rgb bool, err error) {
	switch drm {
	case capture.FourccXRGB8888:
		return vaFourccBGRX, true, nil
	case capture.FourccARGB8888:
		return vaFourccBGRA, true, nil
	case capture.FourccXBGR8888:
		return vaFourccRGBX, true, nil
	case capture.FourccABGR8888:
		return vaFourccRGBA, true, nil
	case capture.FourccNV12:
		return vaFourccNV12, false, nil
	case capture.FourccP010:
		return vaFourccP010, false, nil
	}
	return 0, false, fmt.Errorf("%w: DRM format %#x", encode.ErrUnsupported, drm)
}

// importFrame makes a VA surface of the frame's DMA-BUF once per buffer
// of its pool.
func (e *Encoder) importFrame(d *capture.DMABuf, fourcc uint32, rgb bool) (uint32, error) {
	k := importKey{d.Serial, d.FDs[0]}
	if s, ok := e.imports[k]; ok {
		return s, nil
	}
	if len(e.imports) >= 16 {
		for k, s := range e.imports {
			e.d.destroySurfaces(s)
			delete(e.imports, k)
		}
	}
	desc := &drmPrimeDescriptor{fourcc: fourcc, width: uint32(d.Width), height: uint32(d.Height), numLayers: 1}
	desc.layers[0].drmFormat, desc.layers[0].numPlanes = d.Fourcc, uint32(d.Planes)
	for i := range d.Planes {
		obj := -1
		for j := range int(desc.numObjects) {
			if desc.objects[j].fd == d.FDs[i] {
				obj = j
			}
		}
		if obj < 0 {
			obj = int(desc.numObjects)
			size, _ := syscall.Seek(int(d.FDs[i]), 0, 2)
			desc.objects[obj].fd, desc.objects[obj].size, desc.objects[obj].modifier = d.FDs[i], uint32(size), d.Modifier
			desc.numObjects++
		}
		desc.layers[0].objectIndex[i], desc.layers[0].offset[i], desc.layers[0].pitch[i] = uint32(obj), d.Offsets[i], d.Pitches[i]
	}
	rt := e.rt
	if rgb {
		rt = vaRTFormatRGB32
	}
	s, err := e.d.importSurface(rt, desc)
	if err != nil {
		return 0, err
	}
	e.imports[k] = s
	return s, nil
}

// process copies in, w x h, into the encoder's input surface on the GPU,
// converting RGB to BT.709 limited-range 4:2:0.
func (e *Encoder) process(in uint32, w, h int, rgb bool) error {
	if !e.haveVPP {
		var err error
		if e.vpp, err = e.d.createConfig(vaProfileNone, vaEntrypointVideoProc, nil); err != nil {
			return fmt.Errorf("%w: no video processor to convert frames: %v", encode.ErrUnsupported, err)
		}
		if e.vppCtx, err = e.d.createContext(e.vpp, e.w, e.h, []uint32{e.input}); err != nil {
			call(lib.destroyConfig, e.d.dpy, uintptr(e.vpp))
			return err
		}
		e.haveVPP = true
	}
	e.inRect = rect{w: uint16(w), h: uint16(h)}
	e.outR = e.inRect
	p := e.pipeline
	clear(p)
	put32(p, ppSurface, in)
	*(*uintptr)(unsafe.Pointer(&p[ppSurfaceRegion])) = uintptr(unsafe.Pointer(&e.inRect))
	put32(p, ppSurfaceStandard, vaProcColorStandardBT709)
	*(*uintptr)(unsafe.Pointer(&p[ppOutputRegion])) = uintptr(unsafe.Pointer(&e.outR))
	put32(p, ppBackground, 0xff000000)
	put32(p, ppOutputStandard, vaProcColorStandardBT709)
	inRange := uint8(vaSourceRangeReduced)
	if rgb {
		inRange = vaSourceRangeFull
	}
	put8(p, ppInputProps+colorPropsRange, inRange)
	put8(p, ppOutputProps+colorPropsRange, vaSourceRangeReduced)
	id, err := e.d.buffer(e.vppCtx, vaProcPipelineParameterBufferType, p)
	if err != nil {
		return err
	}
	defer call(lib.destroyBuffer, e.d.dpy, uintptr(id))
	if err := status("vaBeginPicture (VPP)", call(lib.beginPicture, e.d.dpy, uintptr(e.vppCtx), uintptr(e.input))); err != nil {
		return err
	}
	ids := []uint32{id}
	if err := status("vaRenderPicture (VPP)", call(lib.renderPicture, e.d.dpy, uintptr(e.vppCtx), uintptr(unsafe.Pointer(&ids[0])), 1)); err != nil {
		call(lib.endPicture, e.d.dpy, uintptr(e.vppCtx))
		return err
	}
	return status("vaEndPicture (VPP)", call(lib.endPicture, e.d.dpy, uintptr(e.vppCtx)))
}

// SetBitrate takes effect from the next picture, through a rate control
// reset.
func (e *Encoder) SetBitrate(kbps int) error {
	if kbps <= 0 {
		return fmt.Errorf("vaapi: bitrate %d must be positive", kbps)
	}
	e.cfg.BitrateKbps, e.sendRC = kbps, true
	return nil
}

func (e *Encoder) Close() error {
	if e.d == nil {
		return nil
	}
	e.destroyBuffers()
	for _, s := range e.imports {
		e.d.destroySurfaces(s)
	}
	if e.haveVPP {
		call(lib.destroyContext, e.d.dpy, uintptr(e.vppCtx))
		call(lib.destroyConfig, e.d.dpy, uintptr(e.vpp))
	}
	if e.made.coded {
		call(lib.destroyBuffer, e.d.dpy, uintptr(e.coded))
	}
	if e.made.ctx {
		call(lib.destroyContext, e.d.dpy, uintptr(e.ctx))
	}
	if e.made.surfaces {
		e.d.destroySurfaces(e.input, e.recon[0], e.recon[1])
	}
	if e.made.rgb {
		e.d.destroySurfaces(e.rgb)
	}
	if e.made.config {
		call(lib.destroyConfig, e.d.dpy, uintptr(e.config))
	}
	e.d.close()
	e.d = nil
	return nil
}

var _ encode.Encoder = (*Encoder)(nil)
