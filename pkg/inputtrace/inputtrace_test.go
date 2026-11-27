package inputtrace

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type sample struct {
	X    int    `json:"x"`
	Mode string `json:"mode"`
}

func readRecords(t *testing.T, path string) []map[string]any {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []map[string]any
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("line %q: %v", sc.Text(), err)
		}
		out = append(out, m)
	}
	return out
}

func kinds(recs []map[string]any) []string {
	var k []string
	for _, r := range recs {
		k = append(k, r["kind"].(string))
	}
	return k
}

func TestDisabledEmitDoesNotAllocate(t *testing.T) {
	tr := New(SideClient)
	f := sample{X: 7, Mode: "relative"}
	if a := testing.AllocsPerRun(1000, func() { Emit(tr, "pointer_mode", f) }); a != 0 {
		t.Fatalf("Emit while off allocates %.1f per call", a)
	}
	if a := testing.AllocsPerRun(1000, func() {
		if tr.On() {
			t.Fatal("on")
		}
	}); a != 0 {
		t.Fatalf("On allocates %.1f per call", a)
	}
}

func BenchmarkEmitDisabled(b *testing.B) {
	tr := New(SideHost)
	f := sample{X: 1}
	b.ReportAllocs()
	for b.Loop() {
		Emit(tr, "host_input", f)
	}
}

func TestRecordFormat(t *testing.T) {
	dir := t.TempDir()
	tr := New(SideHost)
	path, err := tr.Enable(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(filepath.Base(path), "input-trace-host-") || !strings.HasSuffix(path, ".jsonl") {
		t.Fatalf("path %q", path)
	}
	if _, err := time.Parse(timeLayout, strings.TrimSuffix(strings.TrimPrefix(filepath.Base(path), "input-trace-host-"), ".jsonl")); err != nil {
		t.Fatalf("path %q has no timestamp: %v", path, err)
	}
	Emit(tr, "cursor_poll", sample{X: 3, Mode: "emit"})
	Emit(tr, "empty", struct{}{})
	if err := tr.Disable(); err != nil {
		t.Fatal(err)
	}
	if tr.On() || tr.Path() != "" {
		t.Fatal("still on after Disable")
	}
	recs := readRecords(t, path)
	if got := strings.Join(kinds(recs), ","); got != "trace_start,cursor_poll,empty,trace_stop" {
		t.Fatalf("kinds %s", got)
	}
	r := recs[1]
	ts, err := time.Parse(time.RFC3339Nano, r["ts"].(string))
	if err != nil || ts.Location() != time.UTC {
		t.Fatalf("ts %v: %v", r["ts"], err)
	}
	if _, ok := r["t_ns"].(float64); !ok || r["side"] != "host" || r["x"] != float64(3) || r["mode"] != "emit" {
		t.Fatalf("record %v", r)
	}
	if recs[2]["t_ns"].(float64) < r["t_ns"].(float64) {
		t.Fatal("t_ns went backwards")
	}
}

// Emit never waits for the writer: with the queue full it drops, and the
// trace says how many it dropped.
func TestFullQueueDropsAndCounts(t *testing.T) {
	dir := t.TempDir()
	tr := New(SideClient)
	tr.queue = 4
	tr.hold = make(chan struct{})
	path, err := tr.Enable(dir)
	if err != nil {
		t.Fatal(err)
	}
	Emit(tr, "x", sample{})
	start := time.Now()
	for range 10000 {
		Emit(tr, "client_input", sample{X: 1})
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("10000 Emits took %v", d)
	}
	close(tr.hold)
	dropped := tr.Dropped()
	if dropped == 0 {
		t.Fatal("nothing dropped with a full queue")
	}
	if err := tr.Disable(); err != nil {
		t.Fatal(err)
	}
	var total, stopped float64
	var written int
	for _, r := range readRecords(t, path) {
		switch r["kind"] {
		case "trace_dropped":
			total = r["total"].(float64)
		case "trace_stop":
			stopped = r["dropped"].(float64)
		case "client_input", "x":
			written++
		}
	}
	if uint64(total) != dropped || stopped != total {
		t.Fatalf("trace_dropped total %v, trace_stop %v, dropped %d", total, stopped, dropped)
	}
	// trace_start took one of the queue's places.
	if written+int(dropped) != 10001 || written != tr.queue-1 {
		t.Fatalf("%d written + %d dropped, want %d + %d", written, dropped, tr.queue-1, 10001-tr.queue+1)
	}
}

func TestRotationKeepsNewestFive(t *testing.T) {
	dir := t.TempDir()
	tr := New(SideClient)
	tr.maxBytes = 4 << 10
	// Another side's files and other files are left alone.
	other := filepath.Join(dir, "input-trace-host-20200101-000000.jsonl")
	unrelated := filepath.Join(dir, "planck.log")
	for _, p := range []string{other, unrelated} {
		if err := os.WriteFile(p, []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tr.Enable(dir); err != nil {
		t.Fatal(err)
	}
	pad := strings.Repeat("p", 200)
	for i := range 400 {
		Emit(tr, "client_input", sample{X: i, Mode: pad})
		if i%50 == 0 {
			time.Sleep(time.Millisecond)
		}
	}
	if err := tr.Disable(); err != nil {
		t.Fatal(err)
	}
	files := traceFiles(dir, SideClient)
	if len(files) != KeepFiles {
		t.Fatalf("%d client files kept, want %d: %v", len(files), KeepFiles, files)
	}
	last := filepath.Join(dir, files[len(files)-1].name)
	for _, f := range files {
		st, err := os.Stat(filepath.Join(dir, f.name))
		if err != nil {
			t.Fatal(err)
		}
		if st.Size() > tr.maxBytes {
			t.Fatalf("%s is %d bytes, cap %d", f.name, st.Size(), tr.maxBytes)
		}
	}
	// The newest file holds the last records, whole lines only.
	recs := readRecords(t, last)
	var lastX float64 = -1
	for _, r := range recs {
		if r["kind"] == "client_input" {
			lastX = r["x"].(float64)
		}
	}
	if lastX != 399 {
		t.Fatalf("last file ends at x=%v", lastX)
	}
	for _, p := range []string{other, unrelated} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s removed: %v", p, err)
		}
	}
}

// Each Enable starts a file of its own and prunes the side to KeepFiles.
func TestEnableStartsAFileAndPrunes(t *testing.T) {
	dir := t.TempDir()
	tr := New(SideHost)
	var paths []string
	for range 7 {
		p, err := tr.Enable(dir)
		if err != nil {
			t.Fatal(err)
		}
		again, err := tr.Enable(dir)
		if err != nil || again != p {
			t.Fatalf("Enable while on: %q, %v; want %q", again, err, p)
		}
		paths = append(paths, p)
		if err := tr.Disable(); err != nil {
			t.Fatal(err)
		}
	}
	if tr.Epoch() != 7 {
		t.Fatalf("epoch %d after 7 enables", tr.Epoch())
	}
	seen := map[string]bool{}
	for _, p := range paths {
		if seen[p] {
			t.Fatalf("%s used twice", p)
		}
		seen[p] = true
	}
	files := traceFiles(dir, SideHost)
	if len(files) != KeepFiles || filepath.Join(dir, files[len(files)-1].name) != paths[len(paths)-1] {
		t.Fatalf("kept %v", files)
	}
}

func TestTraceFilesOrder(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{
		"input-trace-client-20261127-101010-2.jsonl",
		"input-trace-client-20261127-101010.jsonl",
		"input-trace-client-20261127-101009.jsonl",
		"input-trace-client-20261127-101010-10.jsonl",
		"input-trace-client-bogus.jsonl",
		"input-trace-client-20261127-101010x.jsonl",
	} {
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var got []string
	for _, f := range traceFiles(dir, SideClient) {
		got = append(got, f.name)
	}
	want := "input-trace-client-20261127-101009.jsonl input-trace-client-20261127-101010.jsonl input-trace-client-20261127-101010-2.jsonl input-trace-client-20261127-101010-10.jsonl"
	if strings.Join(got, " ") != want {
		t.Fatalf("order %v", got)
	}
}

// Records emitted while enabling and disabling from other goroutines
// neither panic nor race.
func TestConcurrentEmitAndToggle(t *testing.T) {
	dir := t.TempDir()
	tr := New(SideClient)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
				Emit(tr, "client_input", sample{X: 1})
			}
		}
	}()
	for range 20 {
		if _, err := tr.Enable(dir); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
		if err := tr.Disable(); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	<-done
}

func TestEnvOn(t *testing.T) {
	for v, want := range map[string]bool{"1": true, "true": true, "0": false, "": false, "yes": false} {
		t.Setenv(Env, v)
		if EnvOn() != want {
			t.Errorf("%s=%q: EnvOn %v", Env, v, !want)
		}
	}
}
