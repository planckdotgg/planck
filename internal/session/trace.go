package session

import (
	"bufio"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
)

// Per-frame stage traces for latency audits. Setting PLANCK_TRACE_HOST or
// PLANCK_TRACE_CLIENT to a file path records one row per frame in memory
// (no allocation per frame until traceCap rows) and writes a CSV when the
// stream or connection ends. cmd/planck-trace summarizes them. All times
// are transport.Mono nanoseconds of the process that wrote the row.
const (
	traceHostEnv   = "PLANCK_TRACE_HOST"
	traceClientEnv = "PLANCK_TRACE_CLIENT"
	traceCap       = 1 << 17
)

// hostTraceRow is one encoded frame on the host.
type hostTraceRow struct {
	Frame     uint32
	Key       bool
	Bytes     int
	WaitStart int64 // the video loop asked the source for a frame
	Present   int64 // the compositor presented it (synth: = Acquired)
	Acquired  int64 // the source returned it
	EncStart  int64
	EncEnd    int64
	// FirstSlice is when NVENC's first sub-frame slice became readable, 0
	// without sub-frame readback.
	FirstSlice int64
	SendEnd    int64 // the last datagram of the frame was written
	Accum      int   // DDA AccumulatedFrames
}

// clientTraceRow is one frame on the client. Host stamps are mapped onto
// the client clock with the offset current at that frame.
type clientTraceRow struct {
	Frame     uint32
	Key       bool
	Bytes     int
	Present   int64 // host compositor present, mapped
	EncEnd    int64 // host encode end, mapped
	FirstPkt  int64 // first datagram of the frame read from the socket
	Complete  int64 // last needed datagram reassembled (onFrame)
	QueueLen  int   // frames waiting in front of it when queued
	Dequeue   int64 // the decode loop took it
	DecodeNs  int64 // the sink's decode latency
	PresentNs int64 // the sink's present call
	Done      int64 // Submit returned
	OffsetNs  int64 // host clock minus client clock
	RTTNs     int64
	Err       bool
}

type tracer[T any] struct {
	path string
	mu   sync.Mutex
	rows []T
}

func newTracer[T any](env string) *tracer[T] {
	p := os.Getenv(env)
	if p == "" {
		return nil
	}
	return &tracer[T]{path: p, rows: make([]T, 0, traceCap)}
}

func (t *tracer[T]) add(r T) {
	if t == nil {
		return
	}
	t.mu.Lock()
	if len(t.rows) < cap(t.rows) {
		t.rows = append(t.rows, r)
	}
	t.mu.Unlock()
}

// flush writes the rows as CSV, one column per field.
func (t *tracer[T]) flush() error {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	f, err := os.Create(t.path)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	typ := reflect.TypeFor[T]()
	names := make([]string, typ.NumField())
	for i := range names {
		names[i] = strings.ToLower(typ.Field(i).Name)
	}
	fmt.Fprintln(w, strings.Join(names, ","))
	for i := range t.rows {
		v := reflect.ValueOf(&t.rows[i]).Elem()
		for j := 0; j < v.NumField(); j++ {
			if j > 0 {
				w.WriteByte(',')
			}
			fmt.Fprint(w, v.Field(j).Interface())
		}
		w.WriteByte('\n')
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	t.rows = t.rows[:0]
	return f.Close()
}
