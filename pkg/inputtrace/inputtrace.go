// Package inputtrace records what the client and the host do with the
// pointer and the input around it, one JSON object a line, for debugging a
// cursor that goes astray. Off, it costs an atomic load: callers guard with
// On, or call Emit, which checks it before boxing its fields.
//
// Each record has ts (UTC, RFC 3339 with nanoseconds), t_ns (the process's
// transport.Mono clock, which the latency traces use too), side and kind,
// then the fields of its kind. Records go through a bounded queue to a
// goroutine that writes them; a full queue drops them, and the writer notes
// how many with a trace_dropped record.
package inputtrace

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/planckdotgg/planck/pkg/transport"
)

// Side is which end of the stream writes a trace.
type Side string

const (
	SideClient Side = "client"
	SideHost   Side = "host"
)

const (
	// MaxFileBytes is how large a trace file grows before the next one
	// starts.
	MaxFileBytes = 20 << 20
	// KeepFiles is how many trace files of a side stay, the newest.
	KeepFiles = 5
	// Env turns tracing on for a run, as the -input-trace flag does.
	Env = "PLANCK_INPUT_TRACE"

	queueLen   = 8192
	flushEvery = 250 * time.Millisecond
	timeLayout = "20060102-150405"
)

// Client and Host are the process's tracers. A client app that hosts too
// writes both, each to its own files.
var (
	Client = New(SideClient)
	Host   = New(SideHost)
)

// EnvOn reports whether PLANCK_INPUT_TRACE asks for tracing.
func EnvOn() bool {
	on, err := strconv.ParseBool(os.Getenv(Env))
	return err == nil && on
}

// Dir is where traces go under a config directory.
func Dir(configDir string) string { return filepath.Join(configDir, "logs") }

// Tracer writes one side's trace while enabled. Its methods are safe from
// any goroutine.
type Tracer struct {
	side  Side
	on    atomic.Bool
	epoch atomic.Uint32
	cur   atomic.Pointer[sink]

	// mu orders Enable and Disable.
	mu sync.Mutex
	// maxBytes, keep and queue are MaxFileBytes, KeepFiles and queueLen,
	// smaller in tests.
	maxBytes int64
	keep     int
	queue    int
	// hold, in tests, keeps a new writer from starting until it closes.
	hold chan struct{}
	// lastStamp and lastSeq name the side's last file, which the next
	// sorts after. Enable and the writer it starts take turns at them.
	lastStamp string
	lastSeq   int
}

// New is a tracer for side, off.
func New(side Side) *Tracer {
	return &Tracer{side: side, maxBytes: MaxFileBytes, keep: KeepFiles, queue: queueLen}
}

// On reports whether the tracer records.
func (t *Tracer) On() bool { return t.on.Load() }

// Epoch counts the times tracing turned on, so that a caller can write the
// context a trace needs at its start: it changes with each Enable that was
// off.
func (t *Tracer) Epoch() uint32 { return t.epoch.Load() }

// Path is the file the tracer writes now, "" while off.
func (t *Tracer) Path() string {
	if s := t.cur.Load(); s != nil {
		return s.path()
	}
	return ""
}

// Enable starts a trace file in dir, keeping the newest KeepFiles of the
// side, and returns its path. Enabled already, it keeps the file it has.
func (t *Tracer) Enable(dir string) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if s := t.cur.Load(); s != nil {
		return s.path(), nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("inputtrace: %w", err)
	}
	s := &sink{t: t, dir: dir, ch: make(chan entry, t.queue), stop: make(chan struct{}), done: make(chan struct{})}
	if err := s.open(); err != nil {
		return "", err
	}
	t.cur.Store(s)
	t.epoch.Add(1)
	t.on.Store(true)
	go func(hold chan struct{}) {
		if hold != nil {
			<-hold
		}
		s.run()
	}(t.hold)
	Emit(t, "trace_start", traceStart{PID: os.Getpid(), Path: s.path(), MaxBytes: t.maxBytes, Keep: t.keep})
	return s.path(), nil
}

// Disable stops recording and returns once every record queued is on
// disk and the file is closed.
func (t *Tracer) Disable() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.cur.Load()
	if s == nil {
		return nil
	}
	t.on.Store(false)
	t.cur.Store(nil)
	close(s.stop)
	<-s.done
	return s.err
}

// Dropped is how many records the current trace dropped on a full queue.
func (t *Tracer) Dropped() uint64 {
	if s := t.cur.Load(); s != nil {
		return s.dropped.Load()
	}
	return 0
}

// Emit queues a record of kind with fields, which marshal to a JSON
// object, unless the tracer is off. It never blocks: a full queue drops
// the record. Fields are marshaled on the writer's goroutine, so they must
// not share memory the caller changes.
func Emit[T any](t *Tracer, kind string, fields T) {
	if !t.on.Load() {
		return
	}
	s := t.cur.Load()
	if s == nil {
		return
	}
	e := entry{wall: time.Now(), mono: transport.Mono(), kind: kind, fields: fields}
	select {
	case s.ch <- e:
	default:
		s.dropped.Add(1)
	}
}

type entry struct {
	wall   time.Time
	mono   int64
	kind   string
	fields any
}

type traceStart struct {
	PID      int    `json:"pid"`
	Path     string `json:"path"`
	MaxBytes int64  `json:"max_bytes"`
	Keep     int    `json:"keep"`
}

type traceStop struct {
	Dropped uint64 `json:"dropped"`
}

type traceDropped struct {
	Dropped uint64 `json:"dropped"`
	Total   uint64 `json:"total"`
}

// sink is one enabled stretch of a trace: its queue, the writer goroutine
// and the file it writes, which rotates at maxBytes.
type sink struct {
	t       *Tracer
	dir     string
	ch      chan entry
	stop    chan struct{}
	done    chan struct{}
	dropped atomic.Uint64

	// The rest is the writer's alone, but for name, which Path reads.
	nameMu sync.Mutex
	name   string
	f      *os.File
	w      *bufio.Writer
	size   int64
	noted  uint64
	line   []byte
	err    error
}

func (s *sink) path() string {
	s.nameMu.Lock()
	defer s.nameMu.Unlock()
	return s.name
}

func (s *sink) run() {
	defer close(s.done)
	tick := time.NewTicker(flushEvery)
	defer tick.Stop()
	for {
		select {
		case e := <-s.ch:
			s.write(e)
		case <-tick.C:
			s.noteDrops()
			s.flush()
		case <-s.stop:
			for drained := false; !drained; {
				select {
				case e := <-s.ch:
					s.write(e)
				default:
					drained = true
				}
			}
			s.noteDrops()
			s.write(entry{wall: time.Now(), mono: transport.Mono(), kind: "trace_stop", fields: traceStop{Dropped: s.noted}})
			s.flush()
			if err := s.f.Close(); err != nil && s.err == nil {
				s.err = err
			}
			return
		}
	}
}

func (s *sink) noteDrops() {
	if n := s.dropped.Load(); n != s.noted {
		s.write(entry{wall: time.Now(), mono: transport.Mono(), kind: "trace_dropped", fields: traceDropped{Dropped: n - s.noted, Total: n}})
		s.noted = n
	}
}

func (s *sink) flush() {
	if s.err != nil {
		return
	}
	if err := s.w.Flush(); err != nil && s.err == nil {
		s.err = err
	}
}

// write adds e to the file, unless an earlier write, flush or rotation
// failed: the trace stops there and Disable reports why.
func (s *sink) write(e entry) {
	if s.err != nil {
		return
	}
	s.line = s.encode(s.line[:0], e)
	if s.size > 0 && s.size+int64(len(s.line)) > s.t.maxBytes {
		if s.rotate(); s.err != nil {
			return
		}
	}
	n, err := s.w.Write(s.line)
	s.size += int64(n)
	if err != nil && s.err == nil {
		s.err = err
	}
}

func (s *sink) encode(b []byte, e entry) []byte {
	b = append(b, `{"ts":"`...)
	b = e.wall.UTC().AppendFormat(b, time.RFC3339Nano)
	b = append(b, `","t_ns":`...)
	b = strconv.AppendInt(b, e.mono, 10)
	b = append(b, `,"side":"`...)
	b = append(b, s.t.side...)
	b = append(b, `","kind":`...)
	b = strconv.AppendQuote(b, e.kind)
	if js, err := json.Marshal(e.fields); err != nil {
		b = append(b, `,"marshal_error":`...)
		b = strconv.AppendQuote(b, err.Error())
	} else if len(js) > 2 && js[0] == '{' {
		b = append(b, ',')
		b = append(b, js[1:len(js)-1]...)
	}
	return append(b, '}', '\n')
}

// open starts a new file, named for now, and prunes the side's old ones.
func (s *sink) open() error {
	name, f, err := s.create(time.Now())
	if err != nil {
		return fmt.Errorf("inputtrace: %w", err)
	}
	s.nameMu.Lock()
	s.name = name
	s.nameMu.Unlock()
	s.f, s.size = f, 0
	if s.w == nil {
		s.w = bufio.NewWriterSize(f, 64<<10)
	} else {
		s.w.Reset(f)
	}
	prune(s.dir, s.t.side, s.t.keep, name)
	return nil
}

func (s *sink) rotate() {
	s.flush()
	old := s.f
	if err := s.open(); err != nil {
		if s.err == nil {
			s.err = err
		}
		return
	}
	if err := old.Close(); err != nil && s.err == nil {
		s.err = err
	}
}

// maxSeq bounds the files tried for one second's trace before create gives
// up.
const maxSeq = 1000

// create makes a new file for a trace started at now, named to sort after
// the side's last one: a second's second file takes a sequence number. Only
// a name that exists already moves it to the next; any other error ends it.
func (s *sink) create(now time.Time) (string, *os.File, error) {
	t := s.t
	stamp := now.Format(timeLayout)
	seq := 0
	if stamp <= t.lastStamp {
		stamp, seq = t.lastStamp, t.lastSeq+1
	}
	base := filepath.Join(s.dir, "input-trace-"+string(t.side)+"-"+stamp)
	for end := seq + maxSeq; seq < end; seq++ {
		name := base + ".jsonl"
		if seq > 0 {
			name = base + "-" + strconv.Itoa(seq) + ".jsonl"
		}
		f, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", nil, err
		}
		t.lastStamp, t.lastSeq = stamp, seq
		return name, f, nil
	}
	return "", nil, fmt.Errorf("%s: %d names taken", base, maxSeq)
}

// traceFile is a trace file of a side, ordered by when it started.
type traceFile struct {
	name  string
	stamp string
	seq   int
}

// traceFiles lists the side's trace files in dir, oldest first.
func traceFiles(dir string, side Side) []traceFile {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	prefix := "input-trace-" + string(side) + "-"
	var out []traceFile
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || !strings.HasPrefix(n, prefix) || !strings.HasSuffix(n, ".jsonl") {
			continue
		}
		rest := strings.TrimSuffix(strings.TrimPrefix(n, prefix), ".jsonl")
		if len(rest) < len(timeLayout) {
			continue
		}
		tf := traceFile{name: n, stamp: rest[:len(timeLayout)]}
		if _, err := time.Parse(timeLayout, tf.stamp); err != nil {
			continue
		}
		if seq := rest[len(timeLayout):]; seq != "" {
			v, err := strconv.Atoi(strings.TrimPrefix(seq, "-"))
			if err != nil || seq[0] != '-' {
				continue
			}
			tf.seq = v
		}
		out = append(out, tf)
	}
	slices.SortFunc(out, func(a, b traceFile) int {
		if c := strings.Compare(a.stamp, b.stamp); c != 0 {
			return c
		}
		return a.seq - b.seq
	})
	return out
}

// prune removes all but the newest keep trace files of side, never cur.
func prune(dir string, side Side, keep int, cur string) {
	files := traceFiles(dir, side)
	for i := 0; i < len(files)-keep; i++ {
		if p := filepath.Join(dir, files[i].name); p != cur {
			os.Remove(p)
		}
	}
}
