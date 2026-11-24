package session

import (
	"encoding/csv"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// cmd/planck-trace reads these columns by name.
func TestTraceColumnsMatchPlanckTrace(t *testing.T) {
	check := func(t *testing.T, header []string, want ...string) {
		for _, c := range want {
			if !slices.Contains(header, c) {
				t.Errorf("trace header %v has no column %q", header, c)
			}
		}
	}
	dir := t.TempDir()
	read := func(t *testing.T, path string) [][]string {
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		recs, err := csv.NewReader(f).ReadAll()
		if err != nil {
			t.Fatal(err)
		}
		return recs
	}

	t.Setenv(traceHostEnv, filepath.Join(dir, "host.csv"))
	h := newTracer[hostTraceRow](traceHostEnv)
	h.add(hostTraceRow{Frame: 7, Key: true, Bytes: 1000, EncEnd: 42})
	if err := h.flush(); err != nil {
		t.Fatal(err)
	}
	recs := read(t, filepath.Join(dir, "host.csv"))
	if len(recs) != 2 || recs[1][0] != "7" || recs[1][1] != "true" {
		t.Fatalf("host trace = %v", recs)
	}
	check(t, recs[0], "frame", "key", "bytes", "present", "acquired", "encstart", "encend", "firstslice", "sendend")

	t.Setenv(traceClientEnv, filepath.Join(dir, "client.csv"))
	c := newTracer[clientTraceRow](traceClientEnv)
	c.add(clientTraceRow{Frame: 7})
	if err := c.flush(); err != nil {
		t.Fatal(err)
	}
	check(t, read(t, filepath.Join(dir, "client.csv"))[0],
		"err", "encend", "firstpkt", "done", "complete", "dequeue", "queuelen", "decodens", "presentns", "present", "rttns")

	t.Setenv(traceHostEnv, "")
	if newTracer[hostTraceRow](traceHostEnv) != nil {
		t.Error("tracer made without its environment variable")
	}
}
