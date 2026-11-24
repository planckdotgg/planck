package pyrowave

import "fmt"

const (
	Levels         = 5
	alignment      = 1 << Levels
	minImageSize   = 4 << Levels
	MaxDimension   = 1 << 14
	BlockSize      = 32
	maxBands       = Levels * 3 * 4
	criticalLevels = 2 // levels 4 and 3 hold the coarse picture
)

// Band is one coded sub-band of one component.
type Band struct {
	Level, Component, Kind int // Kind: 0 LL, 1 HL, 2 LH, 3 HH
	Width, Height          int
	BlocksX, BlocksY       int
	// FirstBlock is the block_index of the band's top-left 32x32 block.
	FirstBlock int
}

func (b Band) Blocks() int { return b.BlocksX * b.BlocksY }

// Layout is the coefficient geometry of one picture size and chroma mode.
type Layout struct {
	Width, Height               int
	AlignedWidth, AlignedHeight int
	Chroma420                   bool
	// Bands are in block_index order (level 4 to 0, Y Cb Cr, LL HL LH HH).
	Bands       []Band
	TotalBlocks int
	// CriticalBlocks is the number of leading blocks (levels 4 and 3) whose
	// loss is visible as more than local blur.
	CriticalBlocks int
}

func align(v, a int) int { return (v + a - 1) &^ (a - 1) }

// NewLayout builds the layout of a width x height picture.
func NewLayout(width, height int, chroma420 bool) (*Layout, error) {
	if width < 1 || height < 1 || width > MaxDimension || height > MaxDimension {
		return nil, fmt.Errorf("pyrowave: size %dx%d out of range", width, height)
	}
	if chroma420 && (width%2 != 0 || height%2 != 0) {
		return nil, fmt.Errorf("pyrowave: 4:2:0 needs an even size, got %dx%d", width, height)
	}
	l := &Layout{
		Width: width, Height: height, Chroma420: chroma420,
		AlignedWidth:  max(align(width, alignment), minImageSize),
		AlignedHeight: max(align(height, alignment), minImageSize),
	}
	block := 0
	for level := Levels - 1; level >= 0; level-- {
		for c := 0; c < 3; c++ {
			if level == 0 && c != 0 && chroma420 {
				continue
			}
			first := 1
			if level == Levels-1 {
				first = 0
			}
			for kind := first; kind < 4; kind++ {
				w, h := l.AlignedWidth>>(level+1), l.AlignedHeight>>(level+1)
				b := Band{Level: level, Component: c, Kind: kind, Width: w, Height: h,
					BlocksX: (w + 31) / 32, BlocksY: (h + 31) / 32, FirstBlock: block}
				block += b.Blocks()
				l.Bands = append(l.Bands, b)
			}
		}
		if level == Levels-criticalLevels {
			l.CriticalBlocks = block
		}
	}
	l.TotalBlocks = block
	return l, nil
}

// BandOf returns the index into Bands of the band holding block, or -1.
func (l *Layout) BandOf(block int) int {
	lo, hi := 0, len(l.Bands)
	for lo < hi {
		m := (lo + hi) / 2
		if l.Bands[m].FirstBlock+l.Bands[m].Blocks() <= block {
			lo = m + 1
		} else {
			hi = m
		}
	}
	if lo == len(l.Bands) || block < 0 {
		return -1
	}
	return lo
}

// FirstLevel is the finest decomposition level of component c: chroma
// starts at level 1 under 4:2:0, where its plane is half size.
func (l *Layout) FirstLevel(c int) int {
	if c != 0 && l.Chroma420 {
		return 1
	}
	return 0
}

// PlaneSize is the size of component c's full-resolution plane.
func (l *Layout) PlaneSize(c int) (int, int) {
	if c != 0 && l.Chroma420 {
		return l.AlignedWidth / 2, l.AlignedHeight / 2
	}
	return l.AlignedWidth, l.AlignedHeight
}
