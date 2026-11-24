package pyrowave_test

import (
	"encoding/binary"
	"math/rand"
	"testing"

	"github.com/planckdotgg/planck/internal/synth"
	"github.com/planckdotgg/planck/pkg/pyrowave"
	"github.com/planckdotgg/planck/pkg/pyrowave/ref"
)

const chunkLen = 1391

func encodeSynth(t testing.TB, w, h int, c420 bool, bpp float64) (*ref.Encoder, []byte, []byte) {
	px := make([]byte, w*h*4)
	synth.BGRA(px, w, h, 4)
	img := ref.FromBGRA8(px, w, h, c420, ref.BT709)
	enc, err := ref.NewEncoder(w, h, c420)
	if err != nil {
		t.Fatal(err)
	}
	recs := enc.Encode(img, int(bpp*float64(w*h)/8))
	return enc, recs, img.BGRA8(ref.BT709)
}

func TestPackChunks(t *testing.T) {
	enc, recs, _ := encodeSynth(t, 1920, 1080, true, 4)
	p, err := pyrowave.NewPacker(enc.L, chunkLen)
	if err != nil {
		t.Fatal(err)
	}
	pk, err := p.Pack(nil, enc.Header, recs)
	if err != nil {
		t.Fatal(err)
	}
	if len(pk.Data) != pk.Chunks*chunkLen || pk.SliceEnds[len(pk.SliceEnds)-1] != len(pk.Data) {
		t.Fatalf("%d bytes in %d chunks, slices end at %v", len(pk.Data), pk.Chunks, pk.SliceEnds)
	}
	if pk.Critical < 1 || len(pk.SliceEnds) > 16 {
		t.Fatalf("critical %d, %d slices", pk.Critical, len(pk.SliceEnds))
	}
	prev := 0
	for _, e := range pk.SliceEnds {
		if e%chunkLen != 0 || (e-prev)/chunkLen > pyrowave.MaxSliceChunks {
			t.Fatalf("slice [%d,%d) is not whole chunks within the limit", prev, e)
		}
		prev = e
	}
	for c := 0; c < pk.Chunks; c++ {
		chunk := pk.Data[c*chunkLen:]
		if _, ok := pyrowave.ParseSequenceHeader(chunk); !ok {
			t.Fatalf("chunk %d does not start with the sequence header", c)
		}
	}
	t.Logf("1080p 4 bpp: %d record bytes -> %d chunks (%.2f%% overhead), %d slices, %d critical",
		len(recs), pk.Chunks, 100*(float64(len(pk.Data))/float64(len(recs))-1), len(pk.SliceEnds), pk.Critical)
}

// TestSplitOversizeBlocks packs at a chunk size smaller than many records,
// so blocks split into several records, and checks the decode is unchanged.
func TestSplitOversizeBlocks(t *testing.T) {
	const w, h = 512, 256
	enc, recs, _ := encodeSynth(t, w, h, false, 16)
	big := 0
	for off := 0; off < len(recs); {
		_, n, _ := pyrowave.NextRecord(recs[off:])
		if n > 300 {
			big++
		}
		off += n
	}
	if big == 0 {
		t.Fatal("no record over 300 bytes to split")
	}
	p, _ := pyrowave.NewPacker(enc.L, 300)
	pk, err := p.Pack(nil, enc.Header, recs)
	if err != nil {
		t.Fatal(err)
	}
	want := mustDecodeRecords(t, w, h, false, recs)
	dec, _ := ref.NewDecoder(w, h, false, 300)
	img, err := dec.Decode(pk.Data)
	if err != nil {
		t.Fatal(err)
	}
	if dec.Parser().Rejected != 0 {
		t.Fatalf("%d records rejected", dec.Parser().Rejected)
	}
	if got := img.BGRA8(ref.BT709); ref.PSNR(want, got) < 200 && string(want) != string(got) {
		t.Fatalf("split decode differs: %.2f dB", ref.PSNR(want, got))
	}
	t.Logf("%d records over 300 B split across %d chunks", big, pk.Chunks)
}

func mustDecodeRecords(t testing.TB, w, h int, c420 bool, recs []byte) []byte {
	dec, err := ref.NewDecoder(w, h, c420, chunkLen)
	if err != nil {
		t.Fatal(err)
	}
	return dec.DecodeRecords(recs).BGRA8(ref.BT709)
}

// TestLossDegradesLocally drops chunks at random and whole critical slices:
// every frame still decodes while any sequence header survives.
func TestLossDegradesLocally(t *testing.T) {
	const w, h = 1280, 720
	enc, recs, src := encodeSynth(t, w, h, true, 4)
	p, _ := pyrowave.NewPacker(enc.L, chunkLen)
	pk, err := p.Pack(nil, enc.Header, recs)
	if err != nil {
		t.Fatal(err)
	}
	dec, _ := ref.NewDecoder(w, h, true, chunkLen)
	full, err := dec.Decode(pk.Data)
	if err != nil {
		t.Fatal(err)
	}
	clean := ref.PSNR(src, full.BGRA8(ref.BT709))
	rng := rand.New(rand.NewSource(7))
	lossy := make([]byte, len(pk.Data))
	for _, rate := range []float64{0.01, 0.05, 0.20} {
		copy(lossy, pk.Data)
		lost := 0
		for c := 0; c < pk.Chunks; c++ {
			if rng.Float64() < rate {
				clear(lossy[c*chunkLen : (c+1)*chunkLen])
				lost++
			}
		}
		img, err := dec.Decode(lossy)
		if err != nil {
			t.Fatalf("%.0f%% loss: %v", rate*100, err)
		}
		psnr := ref.PSNR(src, img.BGRA8(ref.BT709))
		t.Logf("%.0f%% chunk loss (%d of %d): %.2f dB (clean %.2f dB)", rate*100, lost, pk.Chunks, psnr, clean)
		if psnr >= clean || psnr < 10 {
			t.Errorf("%.0f%% loss gave %.2f dB against %.2f clean", rate*100, psnr, clean)
		}
	}
	copy(lossy, pk.Data)
	clear(lossy[:pk.SliceEnds[pk.Critical-1]])
	img, err := dec.Decode(lossy)
	if err != nil {
		t.Fatalf("critical slices lost: %v", err)
	}
	t.Logf("all %d critical slice(s) lost: %.2f dB", pk.Critical, ref.PSNR(src, img.BGRA8(ref.BT709)))
	clear(lossy)
	if _, err := dec.Decode(lossy); err != pyrowave.ErrNoHeader {
		t.Fatalf("every chunk lost: got %v, want ErrNoHeader", err)
	}
}

func TestParserRejects(t *testing.T) {
	l, _ := pyrowave.NewLayout(256, 128, true)
	chunk := make([]byte, 512)
	pyrowave.SequenceHeader{Width: 256, Height: 128}.Put(chunk)
	rec := chunk[8:]
	put := func(off int, h pyrowave.BlockHeader) { h.Put(rec[off:]) }
	// 0: valid one-8x8 record with no planes.
	put(0, pyrowave.BlockHeader{Ballot: 1, PayloadWords: 3, BlockIndex: 0})
	// 12: same block, same 8x8: conflicting ballot.
	put(12, pyrowave.BlockHeader{Ballot: 1, PayloadWords: 3, BlockIndex: 0})
	// 24: block index out of range.
	put(24, pyrowave.BlockHeader{Ballot: 1, PayloadWords: 3, BlockIndex: l.TotalBlocks})
	// 36: code words claim more planes than the record holds.
	put(36, pyrowave.BlockHeader{Ballot: 2, PayloadWords: 3, BlockIndex: 1})
	binary.LittleEndian.PutUint16(rec[44:], 0xffff)
	// 48: payload_words below the header size ends the chunk.
	binary.LittleEndian.PutUint32(rec[48:], 1|1<<16)
	p := pyrowave.NewParser(l, len(chunk))
	up := make([]byte, len(chunk))
	if _, _, err := p.Parse(chunk, up); err != nil {
		t.Fatal(err)
	}
	if len(p.Recs) != 1 || p.Rejected != 4 {
		t.Fatalf("accepted %d, rejected %d; want 1 and 4", len(p.Recs), p.Rejected)
	}
}

func FuzzRecords(f *testing.F) {
	enc, recs, _ := encodeSynth(f, 128, 128, true, 4)
	p, _ := pyrowave.NewPacker(enc.L, 256)
	pk, err := p.Pack(nil, enc.Header, recs)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(pk.Data[:min(len(pk.Data), 1024)])
	dec, _ := ref.NewDecoder(128, 128, true, 256)
	f.Fuzz(func(t *testing.T, b []byte) {
		dec.Decode(b)
	})
}

func TestZeroAllocs(t *testing.T) {
	enc, recs, _ := encodeSynth(t, 1280, 720, true, 4)
	p, _ := pyrowave.NewPacker(enc.L, chunkLen)
	dst := make([]byte, p.MaxFrameLen())
	pk, _ := p.Pack(dst, enc.Header, recs)
	if a := testing.AllocsPerRun(20, func() { p.Pack(dst, enc.Header, recs) }); a != 0 {
		t.Errorf("Pack: %.1f allocs per frame", a)
	}
	parser := pyrowave.NewParser(enc.L, chunkLen)
	up := make([]byte, len(pk.Data))
	if a := testing.AllocsPerRun(20, func() { parser.Parse(pk.Data, up) }); a != 0 {
		t.Errorf("Parse: %.1f allocs per frame", a)
	}
}

func BenchmarkPack1440p(b *testing.B) {
	enc, recs, _ := encodeSynth(b, 2560, 1440, true, 1.6)
	p, _ := pyrowave.NewPacker(enc.L, chunkLen)
	dst := make([]byte, p.MaxFrameLen())
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p.Pack(dst, enc.Header, recs)
	}
}

func BenchmarkParse1440p(b *testing.B) {
	enc, recs, _ := encodeSynth(b, 2560, 1440, true, 1.6)
	p, _ := pyrowave.NewPacker(enc.L, chunkLen)
	pk, _ := p.Pack(nil, enc.Header, recs)
	parser := pyrowave.NewParser(enc.L, chunkLen)
	up := make([]byte, len(pk.Data))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		parser.Parse(pk.Data, up)
	}
}
