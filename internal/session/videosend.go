package session

import (
	"runtime"
	"sync"

	"github.com/planckdotgg/planck/pkg/transport"
	"github.com/planckdotgg/planck/pkg/wire"
)

// videoOut is the part of a transport.Session the video sender uses.
type videoOut interface {
	SendVideoFrame(info wire.VideoInfo, frame []byte) error
	SendVideoSlice(info wire.VideoInfo, slice int, data []byte) error
	SendChunkedFrame(info wire.VideoInfo, frame []byte, ends []int, critical int) error
}

type sendKind uint8

const (
	sendFrame sendKind = iota
	sendSlice
	sendChunked
	// sendEnd closes a frame whose slices went out one by one.
	sendEnd
)

// sendJob is one hand-off from the video loop to the sender. The job that
// ends a frame (a whole frame, or sendEnd) carries the frame's buffer back
// to the pool, and its trace row.
type sendJob struct {
	kind     sendKind
	info     wire.VideoInfo
	slice    int
	data     []byte
	ends     [wire.MaxSlices]int
	nEnds    int
	critical int
	buf      []byte
	// bytes is the encoded frame's size, counted once it went out.
	bytes int
	// sent says a frame went out whole; a sendEnd after an encode error
	// only hands the buffer back.
	sent   bool
	row    hostTraceRow
	traced bool
}

func (j *sendJob) last() bool { return j.kind != sendSlice }

// sendQueueJobs bounds the queue: two frames of the most slices each, and
// the rest of the frame in flight.
const sendQueueJobs = 3 * (wire.MaxSlices + 1)

// videoSender runs FEC, sealing and paced UDP sends on a goroutine of its
// own, so the video loop captures and encodes the next picture while the
// last one is still going out. The loop never waits for it: past
// maxFrames frames queued, the oldest frame not yet started is dropped
// whole and reported to onDrop, so the encoder stops predicting from it.
type videoSender struct {
	out       videoOut
	maxFrames int
	onDrop    func(frameID uint32)
	// onSent runs on the sender goroutine after each frame went out, with
	// its trace row (traced false without one), or the send's error.
	onSent func(j *sendJob, err error)

	mu      sync.Mutex
	ring    [sendQueueJobs]sendJob
	head, n int
	// current is the frame the sender is on; started says it took a job.
	current uint32
	started bool
	closed  bool
	wake    chan struct{}
	done    chan struct{}

	pool chan []byte
}

func newVideoSender(out videoOut, maxFrames int, onDrop func(uint32), onSent func(*sendJob, error)) *videoSender {
	s := &videoSender{out: out, maxFrames: maxFrames, onDrop: onDrop, onSent: onSent,
		wake: make(chan struct{}, 1), done: make(chan struct{}), pool: make(chan []byte, maxFrames+2)}
	go s.run()
	return s
}

// buffer is an empty buffer to encode the next frame into.
func (s *videoSender) buffer() []byte {
	select {
	case b := <-s.pool:
		return b[:0]
	default:
		return make([]byte, 0, 2<<20)
	}
}

func (s *videoSender) recycle(b []byte) {
	if cap(b) == 0 {
		return
	}
	select {
	case s.pool <- b[:0]:
	default:
	}
}

// push queues j without waiting.
func (s *videoSender) push(j *sendJob) {
	var drop [4]uint32
	dropped := drop[:0]
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		s.recycle(j.buf)
		return
	}
	newFrame := s.n == 0 || s.at(s.n-1).info.FrameID != j.info.FrameID
	for (newFrame && s.queuedFrames() >= s.maxFrames) || s.n == len(s.ring) {
		id, ok := s.dropOldestLocked()
		if !ok {
			break
		}
		dropped = append(dropped, id)
	}
	if s.n == len(s.ring) {
		s.mu.Unlock()
		s.recycle(j.buf)
		return
	}
	*s.at(s.n) = *j
	s.n++
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
	for _, id := range dropped {
		s.onDrop(id)
	}
}

func (s *videoSender) at(i int) *sendJob { return &s.ring[(s.head+i)%len(s.ring)] }

// queuedFrames counts the frames with jobs queued, but for the one the
// sender is on.
func (s *videoSender) queuedFrames() int {
	frames := 0
	var prev uint32
	for i := 0; i < s.n; i++ {
		id := s.at(i).info.FrameID
		if (i == 0 || id != prev) && !(s.started && id == s.current) {
			frames++
		}
		prev = id
	}
	return frames
}

// dropOldestLocked removes every queued job of the oldest frame the
// sender has not started.
func (s *videoSender) dropOldestLocked() (uint32, bool) {
	victim, found := uint32(0), false
	for i := 0; i < s.n; i++ {
		if id := s.at(i).info.FrameID; !(s.started && id == s.current) {
			victim, found = id, true
			break
		}
	}
	if !found {
		return 0, false
	}
	kept := 0
	for i := 0; i < s.n; i++ {
		j := s.at(i)
		if j.info.FrameID == victim {
			s.recycle(j.buf)
			continue
		}
		if kept != i {
			*s.at(kept) = *j
		}
		kept++
	}
	for i := kept; i < s.n; i++ {
		*s.at(i) = sendJob{}
	}
	s.n = kept
	return victim, true
}

func (s *videoSender) run() {
	defer close(s.done)
	// The pacer spins between trains of datagrams; preempted for a
	// scheduler quantum, a frame would take twice its share to go out.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if revert, err := boostVideoThread(); err == nil {
		defer revert()
	}
	var j sendJob
	// failed is a frame one of whose slices did not go out.
	var failed uint32
	var anyFailed bool
	for {
		s.mu.Lock()
		for s.n == 0 && !s.closed {
			s.mu.Unlock()
			<-s.wake
			s.mu.Lock()
		}
		if s.n == 0 {
			s.mu.Unlock()
			return
		}
		j = *s.at(0)
		*s.at(0) = sendJob{}
		s.head = (s.head + 1) % len(s.ring)
		s.n--
		s.current, s.started = j.info.FrameID, !j.last()
		closed := s.closed
		s.mu.Unlock()
		if closed {
			s.recycle(j.buf)
			continue
		}
		var err error
		switch j.kind {
		case sendFrame:
			err = s.out.SendVideoFrame(j.info, j.data)
		case sendSlice:
			err = s.out.SendVideoSlice(j.info, j.slice, j.data)
		case sendChunked:
			err = s.out.SendChunkedFrame(j.info, j.data, j.ends[:j.nEnds], j.critical)
		}
		if err != nil {
			failed, anyFailed = j.info.FrameID, true
		}
		if j.last() {
			if err == nil && anyFailed && failed == j.info.FrameID {
				j.sent = false
			}
			anyFailed = false
		}
		if j.last() || err != nil {
			if j.traced {
				j.row.SendEnd = transport.Mono()
			}
			s.onSent(&j, err)
		}
		s.recycle(j.buf)
	}
}

// close drops what is queued and waits for the frame going out.
func (s *videoSender) close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
	<-s.done
}
