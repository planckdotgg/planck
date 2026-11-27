package ui

import "testing"

// TestSettingsInputTrace: Settings › General › Diagnostics flips the input
// trace and saves it at once, and shows where it writes: the folder while
// off, the file while on, either one copied by its button.
func TestSettingsInputTrace(t *testing.T) {
	f := newFake()
	f.trace = InputTraceStatus{Dir: "/cfg/planck/logs", File: "/cfg/planck/logs/input-trace-client-20261127-160000.jsonl"}
	a, tt := newTester(t, f)
	a.screen = ScreenSettings
	tt.Frame()
	if !tt.HasText(inputTraceHelp) || !tt.HasText("DIAGNOSTICS") {
		t.Fatalf("General has no input trace; texts %q", tt.Texts())
	}
	if !tt.HasText(f.trace.Dir) || tt.HasText(f.trace.File) {
		t.Fatalf("off, the path shown is not the folder; texts %q", tt.Texts())
	}
	clickScrolled(t, tt, inputTraceTitle)
	if !f.Settings().InputTrace {
		t.Fatal("switch did not turn the trace on")
	}
	f.mu.Lock()
	saved := len(f.saved)
	f.mu.Unlock()
	if saved == 0 {
		t.Fatal("turning the trace on saved nothing")
	}
	tt.Frame()
	snapshot(t, tt, "settings-input-trace")
	if !tt.HasText(f.trace.File) {
		t.Fatalf("on, the trace file is not shown; texts %q", tt.Texts())
	}
	clickScrolled(t, tt, "Copy path")
	tt.Frame()
	if got := tt.Clipboard(); got != f.trace.File {
		t.Fatalf("copied %q, want %q", got, f.trace.File)
	}
	clickScrolled(t, tt, inputTraceTitle)
	if f.Settings().InputTrace {
		t.Fatal("switch did not turn the trace off")
	}
}

// TestStreamMenuWithoutInputTrace: the trace lives in Settings alone; the
// stream's menu neither shows it nor grows with it on.
func TestStreamMenuWithoutInputTrace(t *testing.T) {
	height := func(on bool) float32 {
		f := streamFake()
		f.settings.InputTrace = on
		f.trace = InputTraceStatus{Dir: "/logs", File: "/logs/input-trace-client-20261127-160000.jsonl"}
		_, tt, _ := newStreamTester(t, f)
		tt.Frame()
		openMenu(t, tt)
		if tt.HasText(inputTraceTitle) || tt.HasText("Copy path") {
			t.Fatalf("the stream menu shows the input trace: %q", tt.Texts())
		}
		info, ok := tt.Access("Stream controls menu")
		if !ok {
			t.Fatal("no stream menu")
		}
		return info.Bounds.H
	}
	if off, on := height(false), height(true); on != off {
		t.Fatalf("stream menu %v tall with the trace on, %v off", on, off)
	}
}
