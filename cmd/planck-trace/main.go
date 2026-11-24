// Command planck-trace summarizes the per-frame stage traces planck-host
// and planck-client write when PLANCK_TRACE_HOST and PLANCK_TRACE_CLIENT
// name a file: p50, p95, p99 and max of each stage, in microseconds.
//
//	planck-trace -host host.csv -client client.csv [-skip 120] [-label name]
package main

import (
	"encoding/csv"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
)

type table struct {
	cols map[string]int
	rows [][]string
}

func load(path string) (*table, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	recs, err := csv.NewReader(f).ReadAll()
	if err != nil {
		return nil, err
	}
	if len(recs) == 0 {
		return nil, fmt.Errorf("%s: empty", path)
	}
	t := &table{cols: map[string]int{}, rows: recs[1:]}
	for i, c := range recs[0] {
		t.cols[c] = i
	}
	return t, nil
}

func (t *table) int(row []string, col string) int64 {
	i, ok := t.cols[col]
	if !ok {
		return 0
	}
	if row[i] == "true" {
		return 1
	}
	v, _ := strconv.ParseInt(row[i], 10, 64)
	return v
}

type stage struct {
	name string
	us   []float64
}

func (s *stage) add(ns int64) { s.us = append(s.us, float64(ns)/1e3) }

func pct(v []float64, p float64) float64 {
	return v[min(len(v)-1, int(p*float64(len(v))))]
}

func main() {
	hostPath := flag.String("host", "", "host trace CSV")
	clientPath := flag.String("client", "", "client trace CSV")
	skip := flag.Int("skip", 120, "frames to skip at the start (warm-up)")
	label := flag.String("label", "", "scenario name for the header")
	flag.Parse()
	if err := run(os.Stdout, *hostPath, *clientPath, *skip, *label); err != nil {
		fmt.Fprintln(os.Stderr, "planck-trace:", err)
		os.Exit(1)
	}
}

func run(w io.Writer, hostPath, clientPath string, skip int, label string) error {
	var order []*stage
	byName := map[string]*stage{}
	get := func(n string) *stage {
		s, ok := byName[n]
		if !ok {
			s = &stage{name: n}
			byName[n] = s
			order = append(order, s)
		}
		return s
	}
	keys := 0
	if hostPath != "" {
		h, err := load(hostPath)
		if err != nil {
			return err
		}
		for i, r := range h.rows {
			if i < skip {
				continue
			}
			present, acq := h.int(r, "present"), h.int(r, "acquired")
			es, ee := h.int(r, "encstart"), h.int(r, "encend")
			if h.int(r, "key") == 1 {
				keys++
			}
			get("host: compositor present -> acquire").add(acq - present)
			get("host: acquire -> encode start").add(es - acq)
			get("host: encode").add(ee - es)
			if fs := h.int(r, "firstslice"); fs > 0 {
				get("host: encode start -> first slice ready").add(fs - es)
			}
			get("host: FEC+AEAD+sendto (whole frame)").add(h.int(r, "sendend") - ee)
			get("host: present -> last datagram sent").add(h.int(r, "sendend") - present)
			get("frame size (KB)").add(h.int(r, "bytes"))
		}
	}
	if clientPath != "" {
		c, err := load(clientPath)
		if err != nil {
			return err
		}
		for i, r := range c.rows {
			if i < skip || c.int(r, "err") == 1 {
				continue
			}
			encEnd, first, done := c.int(r, "encend"), c.int(r, "firstpkt"), c.int(r, "done")
			complete, deq := c.int(r, "complete"), c.int(r, "dequeue")
			if first > 0 {
				get("net: encode end -> first datagram read").add(first - encEnd)
				get("net: first -> last datagram (reassembly)").add(complete - first)
			}
			get("net: encode end -> frame reassembled").add(complete - encEnd)
			get("client: queue wait").add(deq - complete)
			get("client: frames queued ahead (count)").add(c.int(r, "queuelen") * 1000)
			get("client: decode (VT callback)").add(c.int(r, "decodens"))
			if p := c.int(r, "presentns"); p > 0 {
				get("client: present call").add(p)
			}
			get("client: Submit total").add(done - deq)
			get("E2E: host present -> client Submit done").add(done - c.int(r, "present"))
			get("clock: RTT").add(c.int(r, "rttns"))
		}
	}
	if label != "" {
		fmt.Fprintf(w, "### %s\n\n", label)
	}
	fmt.Fprintf(w, "| stage (us unless noted) | n | p50 | p95 | p99 | max |\n|---|---:|---:|---:|---:|---:|\n")
	for _, s := range order {
		if len(s.us) == 0 {
			continue
		}
		v := slices.Clone(s.us)
		slices.Sort(v)
		fmt.Fprintf(w, "| %s | %d | %.0f | %.0f | %.0f | %.0f |\n", s.name, len(v), pct(v, .5), pct(v, .95), pct(v, .99), v[len(v)-1])
	}
	if keys > 0 {
		fmt.Fprintf(w, "\nkeyframes after skip: %d\n", keys)
	}
	return nil
}
