//go:build windows

package nvenc

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/planckdotgg/planck/internal/clock"
	"github.com/planckdotgg/planck/pkg/capture"
	"github.com/planckdotgg/planck/pkg/capture/dxgi"
	"github.com/planckdotgg/planck/pkg/encode"
	"github.com/planckdotgg/planck/pkg/encode/encodetest"
	"github.com/planckdotgg/planck/pkg/gpu"
)

var guard *gpu.PowerGuard

// TestMain applies the power policy before any test makes a device on the
// NVIDIA adapter: the driver reads it when its user-mode driver loads.
// PLANCK_NO_POWER_GUARD=1 measures the driver's default clocks instead.
func TestMain(m *testing.M) {
	code := func() int {
		if a, err := dxgi.FindAdapter("NVIDIA"); err == nil && os.Getenv("PLANCK_NO_POWER_GUARD") == "" {
			guard = gpu.AcquireHighPerf(a)
			fmt.Printf("power guard on %s: %s (reason: %v, executable %s)\n", a.Name, guard.Mode, guard.Reason, guard.Executable)
			defer func() {
				if err := guard.Close(); err != nil {
					fmt.Println("power guard restore:", err)
				}
			}()
		}
		return m.Run()
	}()
	if guard != nil && guard.Mode == gpu.PowerMaxPerformance {
		v, set, err := gpu.ReadPState(guard.Executable)
		fmt.Printf("after Close: PREFERRED_PSTATE set=%v value=%d err=%v\n", set, v, err)
		if err != nil || set {
			code = 1
		}
	}
	os.Exit(code)
}

func open(t *testing.T, d *dxgi.Device, cfg encode.Config, opts Options) *Encoder {
	t.Helper()
	e, err := Open(d.D3D, d.Info, cfg, opts)
	if err != nil {
		t.Fatalf("open %s %s from %s: %v", cfg.Codec, cfg.Chroma, cfg.Source, err)
	}
	t.Cleanup(func() { e.Close() })
	return e
}

func hevc(chroma encode.Chroma, src capture.Format) encode.Config {
	return encode.Config{Codec: encode.HEVC, Chroma: chroma, Source: src, Width: 1920, Height: 1080, FPS: 120, BitrateKbps: 30000}
}

func TestMatrix(t *testing.T) {
	d := encodetest.Device(t, "NVIDIA")
	av1 := hevc(encode.P010, capture.FormatP010)
	av1.Codec = encode.AV1
	for _, cfg := range []encode.Config{
		hevc(encode.NV12, capture.FormatBGRA8),
		hevc(encode.P010, capture.FormatP010),
		hevc(encode.P010, capture.FormatRGB10A2),
		hevc(encode.YUV444P10, capture.FormatRGB10A2),
		av1,
	} {
		t.Run(fmt.Sprintf("%s_%s_from_%s", cfg.Codec, cfg.Chroma, cfg.Source), func(t *testing.T) {
			e := open(t, d, cfg, Options{})
			frames := encodetest.Frames(t, d, cfg.Source, cfg.Width, cfg.Height, 8)
			r := encodetest.Run{Frames: 240, FPS: cfg.FPS}.Encode(t, e, frames)
			t.Logf("back to back: %s; %.0f kbit/s; %.2f allocs/frame", r.Latency, r.Kbps, r.AllocsPerFrame)
			if r.Keyframes < 1 {
				t.Fatal("no keyframe")
			}
			if r.Kbps > float64(cfg.BitrateKbps)*1.25 {
				t.Fatalf("output %.0f kbit/s over the %d CBR target", r.Kbps, cfg.BitrateKbps)
			}
			if r.AllocsPerFrame >= 0.5 {
				t.Fatalf("%.2f allocations per frame on the hot path", r.AllocsPerFrame)
			}
		})
	}
}

// TestPaced is the case the power policy is for: at a paced 120 fps the
// RTX 4080 stayed in P8 in Phase 0 (HEVC 1080p 2.7-3.3 ms p50).
func TestPaced(t *testing.T) {
	d := encodetest.Device(t, "NVIDIA")
	cfg := hevc(encode.NV12, capture.FormatBGRA8)
	e := open(t, d, cfg, Options{})
	frames := encodetest.Frames(t, d, cfg.Source, cfg.Width, cfg.Height, 8)
	r := encodetest.Run{Frames: 600, FPS: cfg.FPS, Paced: true}.Encode(t, e, frames)
	mode := "no guard"
	if guard != nil {
		mode = guard.Mode.String()
	}
	t.Logf("paced 1080p120 HEVC, power %s: %s", mode, r.Latency)
}

func TestSubFrame(t *testing.T) {
	d := encodetest.Device(t, "NVIDIA")
	for _, slices := range []int{4, 8} {
		t.Run(fmt.Sprintf("%d_slices", slices), func(t *testing.T) {
			cfg := hevc(encode.NV12, capture.FormatBGRA8)
			cfg.Slices, cfg.SubFrame = slices, true
			e := open(t, d, cfg, Options{SplitMode: 15})
			frames := encodetest.Frames(t, d, cfg.Source, cfg.Width, cfg.Height, 8)
			var first []float64
			r := encodetest.Run{Frames: 240, FPS: cfg.FPS, Each: func(i int, pkt encode.Packet) {
				if len(pkt.SliceEnds) != slices {
					t.Fatalf("frame %d: %d slice ends, want %d", i, len(pkt.SliceEnds), slices)
				}
				prev := 0
				for _, end := range pkt.SliceEnds {
					if end <= prev {
						t.Fatalf("frame %d: slice ends not increasing: %v", i, pkt.SliceEnds)
					}
					prev = end
				}
				if prev != len(pkt.Data) {
					t.Fatalf("frame %d: last slice ends at %d of %d bytes", i, prev, len(pkt.Data))
				}
				first = append(first, float64(e.FirstSlice())/1e6)
			}}.Encode(t, e, frames)
			mid := first[len(first)/2]
			t.Logf("whole frame %s; first slice ~%.3f ms; %.2f allocs/frame", r.Latency, mid, r.AllocsPerFrame)
			if mid <= 0 || mid > r.Latency.P99 {
				t.Fatalf("first slice at %.3f ms, whole frame p99 %.3f ms", mid, r.Latency.P99)
			}
		})
	}
}

func TestIntraRefreshKeyframesAndInvalidation(t *testing.T) {
	d := encodetest.Device(t, "NVIDIA")
	cfg := hevc(encode.NV12, capture.FormatBGRA8)
	cfg.IntraRefreshPeriod = 60
	e := open(t, d, cfg, Options{})
	if !e.Caps().IntraRefresh {
		t.Skip("no intra refresh")
	}
	frames := encodetest.Frames(t, d, cfg.Source, cfg.Width, cfg.Height, 8)
	var keys []int
	var invalidated bool
	encodetest.Run{Frames: 200, FPS: cfg.FPS, ForceKey: map[int]bool{100: true}, Each: func(i int, pkt encode.Packet) {
		if pkt.Keyframe {
			keys = append(keys, i)
		}
		if pkt.PTS != int64(i) {
			t.Fatalf("frame %d has PTS %d", i, pkt.PTS)
		}
		if i == 150 && e.Caps().RefInvalidation {
			if err := e.InvalidateRefs(148); err != nil {
				t.Fatalf("invalidate: %v", err)
			}
			invalidated = true
		}
		if i == 151 && pkt.Keyframe {
			t.Fatal("reference invalidation produced a keyframe")
		}
	}}.Encode(t, e, frames)
	if fmt.Sprint(keys) != "[0 100]" {
		t.Fatalf("keyframes at %v, want [0 100]: intra refresh must not insert IDRs", keys)
	}
	t.Logf("keyframes at %v; reference invalidation exercised: %v", keys, invalidated)
}

func TestSetBitrate(t *testing.T) {
	d := encodetest.Device(t, "NVIDIA")
	cfg := hevc(encode.NV12, capture.FormatBGRA8)
	cfg.BitrateKbps = 40000
	e := open(t, d, cfg, Options{})
	frames := encodetest.Frames(t, d, cfg.Source, cfg.Width, cfg.Height, 8)
	high := encodetest.Run{Frames: 240, FPS: cfg.FPS}.Encode(t, e, frames)
	if err := e.SetBitrate(8000); err != nil {
		t.Fatal(err)
	}
	low := encodetest.Run{Frames: 240, FPS: cfg.FPS}.Encode(t, e, frames)
	t.Logf("40000 kbit/s target: %.0f out; 8000 target: %.0f out", high.Kbps, low.Kbps)
	if low.Kbps > 8000*1.25 || low.Kbps > high.Kbps/2 {
		t.Fatalf("SetBitrate did not take: %.0f then %.0f kbit/s", high.Kbps, low.Kbps)
	}
	if err := e.SetBitrate(0); err == nil {
		t.Fatal("SetBitrate(0) succeeded")
	}
}

func TestRefusesWhatHardwareLacks(t *testing.T) {
	d := encodetest.Device(t, "NVIDIA")
	cfg := hevc(encode.YUV444P10, capture.FormatRGB10A2)
	cfg.Codec = encode.AV1
	if _, err := Open(d.D3D, d.Info, cfg, Options{}); !errors.Is(err, encode.ErrUnsupported) {
		t.Fatalf("AV1 4:4:4: got %v, want ErrUnsupported", err)
	}
}

func TestPolicyBoundToThisExecutable(t *testing.T) {
	if guard == nil || guard.Mode != gpu.PowerMaxPerformance {
		t.Skipf("power policy not applied: %v", guard)
	}
	exe, _ := os.Executable()
	if guard.Executable != filepath.Base(exe) {
		t.Fatalf("policy bound to %q, not %q", guard.Executable, filepath.Base(exe))
	}
	v, set, err := gpu.ReadPState(guard.Executable)
	if err != nil || !set || v != 1 {
		t.Fatalf("driver profile holds set=%v value=%d err=%v, want prefer maximum performance (1)", set, v, err)
	}
}

// TestSliceStreaming checks that EncodeSlices hands each slice on before
// the picture is done, with exactly the bytes and boundaries of the final
// packet, and logs the locks and the thread's CPU time per frame.
func TestSliceStreaming(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	d := encodetest.Device(t, "NVIDIA")
	for _, slices := range []int{4, 8} {
		t.Run(fmt.Sprintf("%d_slices", slices), func(t *testing.T) {
			cfg := hevc(encode.NV12, capture.FormatBGRA8)
			cfg.Slices, cfg.SubFrame = slices, true
			e := open(t, d, cfg, Options{})
			frames := encodetest.Frames(t, d, cfg.Source, cfg.Width, cfg.Height, 8)
			const n = 240
			dst := make([]byte, 0, 4<<20)
			type got struct {
				index int
				start int
				data  []byte
				key   bool
				at    time.Duration
			}
			var emitted []got
			var early, polls int
			var cpu time.Duration
			for i := 0; i < n; i++ {
				emitted = emitted[:0]
				c0 := threadCPU(t)
				pkt, err := e.EncodeSlices(frames[i%len(frames)], i == 0, dst, func(idx int, s []byte, key bool) error {
					start := int(uintptr(unsafe.Pointer(unsafe.SliceData(s))) - uintptr(unsafe.Pointer(unsafe.SliceData(dst[:1]))))
					emitted = append(emitted, got{idx, start, append([]byte(nil), s...), key, clock.Now()})
					return nil
				})
				done := clock.Now()
				cpu += threadCPU(t) - c0
				if err != nil {
					t.Fatal(err)
				}
				polls += e.polls
				if len(emitted) != slices || len(pkt.SliceEnds) != slices {
					t.Fatalf("frame %d: %d slices emitted, %d slice ends, want %d", i, len(emitted), len(pkt.SliceEnds), slices)
				}
				start := 0
				for j, g := range emitted {
					end := pkt.SliceEnds[j]
					if g.index != j || g.start != start || !bytes.Equal(g.data, pkt.Data[start:end]) {
						t.Fatalf("frame %d slice %d: index %d at %d, %d bytes; packet has it at %d..%d", i, j, g.index, g.start, len(g.data), start, end)
					}
					if g.key != pkt.Keyframe {
						t.Fatalf("frame %d slice %d: keyframe %v, packet %v", i, j, g.key, pkt.Keyframe)
					}
					start = end
				}
				if done-emitted[0].at > 100*time.Microsecond {
					early++
				}
			}
			t.Logf("%d slices: first slice emitted >100 us before the picture was done in %d of %d frames; %.1f locks and %.0f us of thread CPU per frame",
				slices, early, n, float64(polls)/n, float64(cpu.Microseconds())/n)
			if early < n/2 {
				t.Fatalf("the first slice left early in only %d of %d frames", early, n)
			}
		})
	}
}

var procGetThreadTimes = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetThreadTimes")

func threadCPU(t *testing.T) time.Duration {
	var c, x, k, u windows.Filetime
	if r, _, err := procGetThreadTimes.Call(uintptr(windows.CurrentThread()), uintptr(unsafe.Pointer(&c)), uintptr(unsafe.Pointer(&x)),
		uintptr(unsafe.Pointer(&k)), uintptr(unsafe.Pointer(&u))); r == 0 {
		t.Fatal(err)
	}
	ft := func(f windows.Filetime) time.Duration { return time.Duration(int64(f.HighDateTime)<<32|int64(f.LowDateTime)) * 100 }
	return ft(k) + ft(u)
}
