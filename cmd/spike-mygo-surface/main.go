//go:build darwin || windows

// Command spike-mygo-surface measures the vendored MyGo gpusurface: it
// draws synthetic frames on the GPU, as a hardware decoder would leave
// them, and presents each with no CPU copy, reporting submit-to-present
// latency, frame rate, CPU time and Go heap allocations per frame.
package main

import (
	"flag"
	"fmt"
	"os"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/gpusurface"
)

type config struct {
	frames, warmup, fps, width, height, pool int
	format                                   gpusurface.Format
	timings, origin                          bool
	x                                        int // the window's offset, so that test windows do not cover each other
	adapter                                  string
}

func main() {
	var c config
	var format string
	flag.IntVar(&c.frames, "frames", 2400, "frames measured")
	flag.IntVar(&c.warmup, "warmup", 120, "frames presented before measuring")
	flag.IntVar(&c.fps, "fps", 240, "frames submitted a second; 0 submits each as soon as the last is presented")
	flag.IntVar(&c.width, "w", 1920, "frame width in pixels")
	flag.IntVar(&c.height, "h", 1080, "frame height in pixels")
	flag.IntVar(&c.pool, "pool", 4, "frames in the producer's pool")
	flag.StringVar(&format, "format", "nv12", "frame format: bgra, nv12 or p010")
	flag.BoolVar(&c.timings, "timings", true, "record when each frame reached the screen (its callbacks allocate)")
	flag.StringVar(&c.adapter, "adapter", "", "Windows: the GPU whose name contains this, such as 4080 (default: the system's)")
	flag.BoolVar(&c.origin, "origin", false, "place the window at the top-left corner of the screen, which a window of the screen's size then covers")
	flag.Parse()
	switch strings.ToLower(format) {
	case "bgra":
		c.format = gpusurface.FormatBGRA8
	case "nv12":
		c.format = gpusurface.FormatNV12
	case "p010":
		c.format = gpusurface.FormatP010
	default:
		fmt.Fprintf(os.Stderr, "unknown format %q\n", format)
		os.Exit(2)
	}
	if err := run(c); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// bench is a surface in a window of its own with a producer feeding it.
type bench struct {
	host *host
	surf *gpusurface.Surface
	src  *source
}

// newBench makes the window and the surface on the main thread.
func newBench(c config, timings int) (*bench, error) {
	at := 40
	if c.origin {
		at = 0
	}
	h, err := newHost(c.width, c.height, at+c.x, at)
	if err != nil {
		return nil, err
	}
	surf, err := gpusurface.New(gpusurface.Options{Window: h.handle(), Timings: timings, Adapter: c.adapter})
	if err != nil {
		h.close()
		return nil, err
	}
	src, err := newSource(surf, c.format, c.width, c.height, c.pool)
	if err != nil {
		surf.Close()
		h.close()
		return nil, err
	}
	return &bench{host: h, surf: surf, src: src}, nil
}

func (b *bench) frame(i int) error {
	return b.surf.Present(b.src.produce(i))
}

func (b *bench) close() {
	b.src.close()
	b.surf.Close()
	b.host.close()
}

type measured struct {
	elapsed   time.Duration
	cpu       time.Duration
	mallocs   uint64
	errs      int
	firstErr  error
	submitted int
}

func run(c config) error {
	timings := 0
	if c.timings {
		timings = c.warmup + c.frames
	}
	b, err := newBench(c, timings)
	if err != nil {
		return err
	}
	var m measured
	go func() {
		runtime.LockOSThread()
		m = loop(b, c)
		b.host.stop()
	}()
	// The window's run loop turns on the main thread while frames present:
	// without it, Core Animation gave drawables back late and Present
	// waited for one for most of a refresh.
	b.host.run()
	shown := composition(b.surf)
	b.close()
	report(c, b, m, shown)
	return nil
}

// loop presents the warmup frames and then the frames measured, paced at
// c.fps, on a thread of its own.
func loop(b *bench, c config) measured {
	interval := time.Duration(0)
	if c.fps > 0 {
		interval = time.Second / time.Duration(c.fps)
	}
	var m measured
	next := time.Now()
	present := func(i int) {
		if interval > 0 {
			next = next.Add(interval)
			waitUntil(next)
		}
		if err := b.frame(i); err != nil {
			if m.errs == 0 {
				m.firstErr = err
			}
			m.errs++
		}
	}
	for i := range c.warmup {
		present(i)
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	cpu0 := cpuTime()
	start := time.Now()
	next = start
	for i := range c.frames {
		present(c.warmup + i)
	}
	m.elapsed = time.Since(start)
	m.cpu = cpuTime() - cpu0
	runtime.ReadMemStats(&after)
	m.mallocs = after.Mallocs - before.Mallocs
	m.submitted = c.frames
	return m
}

// waitUntil sleeps until a millisecond before t, then spins: sleeps are
// too coarse to pace 240 frames a second.
func waitUntil(t time.Time) {
	if d := time.Until(t) - time.Millisecond; d > 0 {
		time.Sleep(d)
	}
	for time.Now().Before(t) {
	}
}

func report(c config, b *bench, m measured, shown string) {
	w, h := b.surf.Size()
	fmt.Printf("platform        %s/%s, %s %dx%d frames, pool %d, drawable %dx%d\n", runtime.GOOS, runtime.GOARCH, c.format, c.width, c.height, c.pool, w, h)
	fmt.Printf("present         %s\n", presentMode(b.surf))
	fmt.Printf("composition     %s\n", shown)
	target := "uncapped"
	if c.fps > 0 {
		target = fmt.Sprintf("%d fps", c.fps)
	}
	fmt.Printf("target          %s, %d frames after %d warmup\n", target, c.frames, c.warmup)
	fmt.Printf("achieved        %.1f fps submitted (%v)\n", float64(m.submitted)/m.elapsed.Seconds(), m.elapsed.Round(time.Millisecond))
	fmt.Printf("present errors  %d", m.errs)
	if m.firstErr != nil {
		fmt.Printf(" (first: %v)", m.firstErr)
	}
	fmt.Println()
	fmt.Printf("cpu             %.1f us/frame (process user+sys)\n", float64(m.cpu.Microseconds())/float64(c.frames))
	timed := "timings recorded"
	if !c.timings {
		timed = "hot path only, no timings"
	}
	fmt.Printf("go allocs       %.3f /frame (%d over %d frames, %s)\n", float64(m.mallocs)/float64(c.frames), m.mallocs, c.frames, timed)
	if !c.timings {
		return
	}
	all := b.surf.Timings()
	if len(all) <= c.warmup {
		return
	}
	recs := all[c.warmup:]
	var cpuLat, lat []float64
	var firstShown, lastShown int64
	for _, t := range recs {
		cpuLat = append(cpuLat, ms(t.Encoded-t.Submitted))
		if t.Presented == 0 {
			continue
		}
		lat = append(lat, ms(t.Presented-t.Submitted))
		if firstShown == 0 {
			firstShown = t.Presented
		}
		lastShown = t.Presented
	}
	fmt.Printf("present() cpu   %s ms\n", percentiles(cpuLat))
	if len(lat) == 0 {
		fmt.Printf("submit->present not measured: the platform reported no present times\n")
		return
	}
	shownFPS := 0.0
	if lastShown > firstShown {
		shownFPS = float64(len(lat)-1) / (float64(lastShown-firstShown) / 1e9)
	}
	fmt.Printf("presented       %d of %d frames reached the screen (%.1f fps shown)\n", len(lat), len(recs), shownFPS)
	fmt.Printf("submit->present %s ms\n", percentiles(lat))
}

func ms(ns int64) float64 { return float64(ns) / 1e6 }

func percentiles(v []float64) string {
	s := slices.Clone(v)
	slices.Sort(s)
	at := func(p float64) float64 { return s[min(len(s)-1, int(p*float64(len(s))))] }
	return fmt.Sprintf("p50 %.3f  p95 %.3f  p99 %.3f  max %.3f", at(0.50), at(0.95), at(0.99), s[len(s)-1])
}
