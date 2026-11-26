package hostbuf

import (
	"errors"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"

	"github.com/planckdotgg/planck/pkg/capture"
)

// Buffer is one picture's memory. Its DMABuf stays valid, and its planes
// unchanged, until the pool gets it back.
type Buffer struct {
	DMABuf capture.DMABuf
	// Luma and Chroma are the planes, rows of Pitch bytes.
	Luma, Chroma []byte
	Pitch        int
	mem          []byte
	fd           int
	index        int
	busy         bool
}

// Pool hands out buffers of one size and layout, making more as all are
// out, up to its most.
type Pool struct {
	mu            sync.Mutex
	bufs          []*Buffer
	fourcc        uint32
	width, height int
	most          int
}

var serials atomic.Uint64

// ErrExhausted is Get's error when every buffer is out.
var ErrExhausted = errors.New("hostbuf: every picture buffer is in use")

// New makes a pool of width x height pictures of fourcc (NV12 or P010) of
// at most most buffers.
func New(fourcc uint32, width, height, most int) *Pool {
	return &Pool{fourcc: fourcc, width: width, height: height, most: most}
}

// Matches reports whether p holds pictures of this layout.
func (p *Pool) Matches(fourcc uint32, width, height int) bool {
	return p != nil && p.fourcc == fourcc && p.width == width && p.height == height
}

// Get returns a free buffer, made if none is.
func (p *Pool) Get() (*Buffer, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, b := range p.bufs {
		if !b.busy {
			b.busy = true
			return b, nil
		}
	}
	if len(p.bufs) >= p.most {
		return nil, ErrExhausted
	}
	b, err := p.make(len(p.bufs))
	if err != nil {
		return nil, err
	}
	b.busy = true
	p.bufs = append(p.bufs, b)
	return b, nil
}

// Put gives b back.
func (p *Pool) Put(b *Buffer) {
	p.mu.Lock()
	b.busy = false
	p.mu.Unlock()
}

// At is the buffer of index i, as Buffer.Index tells it.
func (p *Pool) At(i int) *Buffer {
	p.mu.Lock()
	defer p.mu.Unlock()
	if i < 0 || i >= len(p.bufs) {
		return nil
	}
	return p.bufs[i]
}

// Index is b's place in its pool.
func (b *Buffer) Index() int { return b.index }

// Close unmaps every buffer; none may be out.
func (p *Pool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, b := range p.bufs {
		syscall.Munmap(b.mem)
		syscall.Close(b.fd)
	}
	p.bufs = nil
}

// Mem is b's whole mapping, luma then chroma.
func (b *Buffer) Mem() []byte { return b.mem }

func (p *Pool) make(index int) (*Buffer, error) {
	bytes := 1
	if p.fourcc == capture.FourccP010 {
		bytes = 2
	}
	pitch := (p.width*bytes + 255) &^ 255
	ch := (p.height + 1) / 2
	size := pitch * (p.height + ch)
	fd, err := memfd("planck-picture")
	if err != nil {
		return nil, err
	}
	if err := syscall.Ftruncate(fd, int64(size)); err != nil {
		syscall.Close(fd)
		return nil, err
	}
	mem, err := syscall.Mmap(fd, 0, size, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		syscall.Close(fd)
		return nil, err
	}
	b := &Buffer{mem: mem, fd: fd, index: index, Pitch: pitch, Luma: mem[:pitch*p.height], Chroma: mem[pitch*p.height:]}
	d := &b.DMABuf
	d.Fourcc, d.Modifier = p.fourcc, capture.ModifierLinear
	d.Width, d.Height, d.Planes = p.width, p.height, 2
	d.FDs = [4]int32{int32(fd), int32(fd)}
	d.Offsets = [4]uint32{0, uint32(pitch * p.height)}
	d.Pitches = [4]uint32{uint32(pitch), uint32(pitch)}
	d.CPU = [4]unsafe.Pointer{unsafe.Pointer(&mem[0]), unsafe.Pointer(&mem[pitch*p.height])}
	d.Memory = true
	d.Serial = serials.Add(1)
	return b, nil
}

func memfd(name string) (int, error) {
	b := append([]byte(name), 0)
	fd, _, errno := syscall.Syscall(sysMemfdCreate, uintptr(unsafe.Pointer(&b[0])), 1 /* MFD_CLOEXEC */, 0)
	if errno != 0 {
		return -1, errno
	}
	return int(fd), nil
}
