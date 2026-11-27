package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/planckdotgg/planck/pkg/config"
	"github.com/planckdotgg/planck/pkg/inputtrace"
)

func privateClientTrace(t *testing.T) *inputtrace.Tracer {
	t.Helper()
	tr := inputtrace.New(inputtrace.SideClient)
	old := itrace
	itrace = tr
	t.Cleanup(func() {
		tr.Disable()
		itrace = old
	})
	return tr
}

// TestBackendInputTraceSetting: the switch starts and stops the trace at
// once, keeps it in client.json, and tells where it writes.
func TestBackendInputTraceSetting(t *testing.T) {
	tr := privateClientTrace(t)
	dir := t.TempDir()
	b, _ := newTestBackend(t, dir, nil)
	logs := filepath.Join(dir, "logs")
	if st := b.InputTrace(); b.Settings().InputTrace || tr.On() || st.Dir != logs || st.File != "" {
		t.Fatalf("off: setting %v, on %v, status %+v", b.Settings().InputTrace, tr.On(), st)
	}
	s := b.Settings()
	s.InputTrace = true
	if err := b.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	st := b.InputTrace()
	if !tr.On() || !strings.HasPrefix(st.File, filepath.Join(logs, "input-trace-client-")) || st.Err != "" {
		t.Fatalf("on: tracing %v, status %+v", tr.On(), st)
	}
	if stored, _ := config.LoadClientConfig(dir); !stored.InputTrace {
		t.Fatal("the setting was not kept")
	}
	s.InputTrace = false
	if err := b.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	if tr.On() || b.InputTrace().File != "" {
		t.Fatal("still tracing after the switch went off")
	}
	if stored, _ := config.LoadClientConfig(dir); stored.InputTrace {
		t.Fatal("the switch off was not kept")
	}

	s.InputTrace = true
	if err := b.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	again, _ := newTestBackend(t, dir, nil)
	if !again.Settings().InputTrace {
		t.Fatal("a new run does not start with the setting kept")
	}
}

// TestBackendInputTraceForced: -input-trace traces the run from the start
// and shows the switch on, but keeps nothing unless the switch flips.
func TestBackendInputTraceForced(t *testing.T) {
	tr := privateClientTrace(t)
	dir := t.TempDir()
	f := newFinder("", nil, nil, t.Logf)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go f.run(ctx)
	b, err := newBackend(backendConfig{dir: dir, name: "test-client", finder: f, tailscale: noTailnet{}, logf: t.Logf, inputTrace: true})
	if err != nil {
		t.Fatal(err)
	}
	if !tr.On() || !b.Settings().InputTrace {
		t.Fatalf("forced run: tracing %v, switch %v", tr.On(), b.Settings().InputTrace)
	}
	s := b.Settings()
	s.AudioMuted = true
	if err := b.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	if stored, _ := config.LoadClientConfig(dir); stored.InputTrace {
		t.Fatal("a forced run kept the trace on for good")
	}
	b.close()
	if tr.On() {
		t.Fatal("still tracing after close")
	}
}

// TestInputTraceClientFlag: -input-trace and PLANCK_INPUT_TRACE=1 each
// trace the run.
func TestInputTraceClientFlag(t *testing.T) {
	for _, c := range []struct {
		args []string
		env  string
		want bool
	}{
		{nil, "", false},
		{[]string{"-input-trace"}, "", true},
		{nil, "1", true},
		{nil, "false", false},
	} {
		t.Setenv(inputtrace.Env, c.env)
		o, err := parseOptions(c.args, nil)
		if err != nil {
			t.Fatal(err)
		}
		if o.inputTrace != c.want {
			t.Errorf("%v %s=%q: inputTrace %v", c.args, inputtrace.Env, c.env, o.inputTrace)
		}
	}
}
