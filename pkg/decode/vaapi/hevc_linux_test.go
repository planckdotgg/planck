package vaapi

import (
	"encoding/binary"
	"errors"
	"hash/crc32"
	"os"
	"testing"
	"unsafe"

	"github.com/planckdotgg/planck/pkg/capture"
	"github.com/planckdotgg/planck/pkg/decode"
	"github.com/planckdotgg/planck/pkg/decode/nvdec"
	"github.com/planckdotgg/planck/pkg/encode"
)

// TestStructLayouts holds the structs to libva 2.20's sizes and offsets,
// as gcc lays them out on linux/amd64.
func TestStructLayouts(t *testing.T) {
	var pic picParamHEVC
	var sl sliceParamHEVC
	var img vaImage
	var desc drmPrimeDescriptor
	for _, c := range []struct {
		name      string
		got, want uintptr
	}{
		{"VAPictureHEVC", unsafe.Sizeof(pictureHEVC{}), 28},
		{"VAPictureParameterBufferHEVC", unsafe.Sizeof(pic), 604},
		{"pic_fields", unsafe.Offsetof(pic.PicFields), 452},
		{"init_qp_minus26", unsafe.Offsetof(pic.InitQPMinus26), 469},
		{"column_width_minus1", unsafe.Offsetof(pic.ColumnWidthMinus1), 476},
		{"slice_parsing_fields", unsafe.Offsetof(pic.SliceParsingFields), 556},
		{"st_rps_bits", unsafe.Offsetof(pic.STRPSBits), 568},
		{"VASliceParameterBufferHEVC", unsafe.Sizeof(sl), 264},
		{"LongSliceFlags", unsafe.Offsetof(sl.LongSliceFlags), 52},
		{"slice_qp_delta", unsafe.Offsetof(sl.QPDelta), 59},
		{"delta_luma_weight_l0", unsafe.Offsetof(sl.DeltaLumaWeightL0), 66},
		{"ChromaOffsetL1", unsafe.Offsetof(sl.ChromaOffsetL1), 216},
		{"five_minus_max_num_merge_cand", unsafe.Offsetof(sl.FiveMinusMaxNumMergeCand), 246},
		{"num_entry_point_offsets", unsafe.Offsetof(sl.NumEntryPointOffsets), 248},
		{"slice_data_num_emu_prevn_bytes", unsafe.Offsetof(sl.DataNumEmuPrevnBytes), 252},
		{"VAIQMatrixBufferHEVC", unsafe.Sizeof(iqMatrixHEVC{}), 1016},
		{"VASurfaceAttrib", unsafe.Sizeof(surfaceAttrib{}), 24},
		{"VAImage", unsafe.Sizeof(img), 120},
		{"VAImage.buf", unsafe.Offsetof(img.buf), 52},
		{"VAImage.offsets", unsafe.Offsetof(img.offsets), 80},
		{"VADRMPRIMESurfaceDescriptor", unsafe.Sizeof(desc), 312},
		{"num_layers", unsafe.Offsetof(desc.numLayers), 80},
		{"layers", unsafe.Offsetof(desc.layers), 84},
	} {
		if c.got != c.want {
			t.Errorf("%s: %d, want %d", c.name, c.got, c.want)
		}
	}
}

func clip(t *testing.T) [][]byte {
	b, err := os.ReadFile("../vt/testdata/hevc_refinv_640x360.bin")
	if err != nil {
		t.Fatal(err)
	}
	var aus [][]byte
	for len(b) > 0 {
		n := binary.BigEndian.Uint32(b[4:])
		aus = append(aus, b[8:8+n])
		b = b[8+n:]
	}
	return aus
}

func sum(f decode.Frame) uint32 {
	d := capture.DMABufOf(f.Surface)
	bytes := 1
	if d.Fourcc == capture.FourccP010 {
		bytes = 2
	}
	h := crc32.NewIEEE()
	for p, rows := range []int{d.Height, (d.Height + 1) / 2} {
		row := d.Width * bytes
		if p == 1 {
			row = (d.Width + 1) / 2 * 2 * bytes
		}
		for r := range rows {
			h.Write(unsafe.Slice((*byte)(unsafe.Add(d.CPU[p], r*int(d.Pitches[p]))), row))
		}
	}
	return h.Sum32()
}

func openOrSkip(t *testing.T, target decode.Target) *HEVCDecoder {
	v, err := OpenHEVC(target, decode.Config{Codec: encode.HEVC})
	if errors.Is(err, decode.ErrUnsupported) {
		t.Skip(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("VA driver: %s on %s", v.Vendor(), v.d.node.Path)
	return v
}

// TestDecodeHEVCMatchesNVDEC decodes NVENC's clip, frames 20 to 22 lost
// and invalidated, into memory, and compares each picture with NVDEC's
// where NVDEC is there: HEVC decoding is exact, so they must be equal.
func TestDecodeHEVCMatchesNVDEC(t *testing.T) {
	v := openOrSkip(t, decode.Target{})
	defer v.Close()
	aus := clip(t)
	var got []uint32
	for i, au := range aus {
		f, err := v.Decode(au, int64(i))
		if err != nil {
			t.Fatalf("access unit %d: %v", i, err)
		}
		if f.Width != 640 || f.Height != 360 || f.Format != capture.FormatNV12 {
			t.Fatalf("access unit %d: %dx%d %v", i, f.Width, f.Height, f.Format)
		}
		got = append(got, sum(f))
		if i == 1 {
			t.Logf("second picture in %v", f.Latency)
		}
		v.Release(f)
	}
	if err := nvdec.Available(); err != nil {
		t.Logf("no NVDEC to compare with: %v", err)
		return
	}
	n, err := nvdec.Open(decode.Config{Codec: encode.HEVC})
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	bad := 0
	for i, au := range aus {
		f, err := n.Decode(au, int64(i))
		if err != nil {
			t.Fatal(err)
		}
		if want := sum(f); got[i] != want {
			bad++
			if bad <= 5 {
				t.Errorf("picture %d: CRC %08x, NVDEC's %08x", i, got[i], want)
			}
		}
		n.Release(f)
	}
	if bad == 0 {
		t.Logf("all %d pictures equal NVDEC's", len(aus))
	}
}

// TestExportsDMABufs decodes with a surface that takes every DMA-BUF.
func TestExportsDMABufs(t *testing.T) {
	v := openOrSkip(t, decode.Target{Accepts: func(uint32, uint64) bool { return true }})
	defer v.Close()
	for i, au := range clip(t)[:4] {
		f, err := v.Decode(au, int64(i))
		if err != nil {
			t.Fatalf("access unit %d: %v", i, err)
		}
		d := capture.DMABufOf(f.Surface)
		if v.copy {
			t.Logf("the driver exports no DMA-BUFs: pictures are copied (%dx%d %s)", d.Width, d.Height, fourcc(d.Fourcc))
		} else if !d.HasFDs() || d.Planes != 2 {
			t.Fatalf("picture %d: %+v", i, d)
		} else if i == 0 {
			t.Logf("exported %s modifier %#x, pitches %d/%d", fourcc(d.Fourcc), d.Modifier, d.Pitches[0], d.Pitches[1])
		}
		v.Release(f)
	}
}

func fourcc(f uint32) string { return string([]byte{byte(f), byte(f >> 8), byte(f >> 16), byte(f >> 24)}) }
