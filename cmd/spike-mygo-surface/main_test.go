//go:build darwin || windows

package main

import (
	"fmt"
	"os"
	"testing"

	"github.com/egoist/mygo/gpusurface"
)

var testBenches = map[gpusurface.Format]*bench{}

// TestMain makes the windows on the main thread and runs the tests on
// another while the main thread runs the windows' event loop.
func TestMain(m *testing.M) {
	var last *bench
	for i, f := range []gpusurface.Format{gpusurface.FormatBGRA8, gpusurface.FormatNV12, gpusurface.FormatP010} {
		b, err := newBench(config{format: f, width: 640, height: 360, pool: 4, x: 680 * i, adapter: os.Getenv("PLANCK_ADAPTER")}, 0)
		if err != nil {
			fmt.Fprintf(os.Stderr, "no GPU surface for %s: %v\n", f, err)
			os.Exit(1)
		}
		testBenches[f], last = b, b
	}
	code := 0
	go func() {
		code = m.Run()
		last.host.stop()
	}()
	last.host.run()
	for _, b := range testBenches {
		b.close()
	}
	os.Exit(code)
}

func TestPresentAllocatesNothing(t *testing.T) {
	for f, b := range testBenches {
		t.Run(f.String(), func(t *testing.T) {
			// The first present of each frame of the pool wraps it.
			for i := range 8 {
				if err := b.frame(i); err != nil {
					t.Fatal(err)
				}
			}
			i := 8
			allocs := testing.AllocsPerRun(240, func() {
				if err := b.frame(i); err != nil {
					t.Fatal(err)
				}
				i++
			})
			if allocs != 0 {
				t.Errorf("produce and present allocate %v times a frame, want 0", allocs)
			}
		})
	}
}

func BenchmarkPresent(b *testing.B) {
	for f, tb := range testBenches {
		b.Run(f.String(), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; b.Loop(); i++ {
				if err := tb.frame(i); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
