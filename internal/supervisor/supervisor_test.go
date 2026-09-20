package supervisor

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"runtimeforge/internal/appdir"
)

type fakeProcess struct {
	pid      int
	done     chan struct{}
	once     sync.Once
	err      error
	mu       sync.Mutex
	signaled []os.Signal
}

func newFakeProcess(pid int) *fakeProcess {
	return &fakeProcess{pid: pid, done: make(chan struct{})}
}

func (p *fakeProcess) PID() int { return p.pid }
func (p *fakeProcess) Wait() error {
	<-p.done
	return p.err
}
func (p *fakeProcess) Signal(s os.Signal) error {
	p.mu.Lock()
	p.signaled = append(p.signaled, s)
	p.mu.Unlock()
	p.finish(nil)
	return nil
}
func (p *fakeProcess) Kill() error {
	p.finish(errors.New("killed"))
	return nil
}
func (p *fakeProcess) finish(err error) { p.once.Do(func() { p.err = err; close(p.done) }) }

func (p *fakeProcess) signalCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.signaled)
}

type fakeStarter struct {
	mu    sync.Mutex
	procs []*fakeProcess
	name  string
	args  []string
	fail  bool
}

func (f *fakeStarter) Start(name string, args, env []string, log func(string)) (Process, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.name = name
	f.args = args
	if f.fail {
		return nil, errors.New("spawn failed")
	}
	if log != nil {
		log("fake server starting: " + name)
	}
	p := newFakeProcess(4242 + len(f.procs))
	f.procs = append(f.procs, p)
	return p, nil
}

func newSup(t *testing.T, starter Starter, health func(context.Context, int) error, timeout time.Duration) *Supervisor {
	t.Helper()
	paths := appdir.New(t.TempDir())
	if err := paths.Ensure(); err != nil {
		t.Fatal(err)
	}
	return New(Options{
		Paths:        paths,
		PortRange:    []int{30000, 30010},
		ReadyTimeout: timeout,
		Starter:      starter,
		HealthCheck:  health,
	})
}

func spec() LoadSpec {
	return LoadSpec{
		ModelID:      "m1",
		ModelName:    "Test Model",
		ModelPath:    "/models/test.gguf",
		Architecture: "llama",
		RuntimeID:    "upstream@abc/cpu",
		Backend:      "cpu",
		ServerBinary: "/runtimes/upstream/abc/cpu/bin/llama-server",
	}
}

func waitState(t *testing.T, s *Supervisor, id string, want State) Instance {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if i, ok := s.Get(id); ok && i.Status == want {
			return i
		}
		time.Sleep(10 * time.Millisecond)
	}
	i, _ := s.Get(id)
	t.Fatalf("state = %s, want %s", i.Status, want)
	return Instance{}
}

func TestLoadBecomesReady(t *testing.T) {
	st := &fakeStarter{}
	s := newSup(t, st, func(context.Context, int) error { return nil }, time.Second)
	inst, err := s.Load(spec())
	if err != nil {
		t.Fatal(err)
	}
	if inst.Status != StateLoading {
		t.Errorf("initial state = %s", inst.Status)
	}
	ready := waitState(t, s, "m1", StateReady)
	if ready.Port < 30000 || ready.Port > 30010 {
		t.Errorf("port = %d not in range", ready.Port)
	}
	if ready.PID == 0 {
		t.Error("pid not set")
	}
	// Args must point llama-server at the model and internal host.
	joined := ""
	for _, a := range ready.Args {
		joined += a + " "
	}
	for _, want := range []string{"-m /models/test.gguf", "--host 127.0.0.1", "--port"} {
		if !contains(joined, want) {
			t.Errorf("args missing %q: %v", want, ready.Args)
		}
	}
}

func TestLoadDuplicate(t *testing.T) {
	st := &fakeStarter{}
	s := newSup(t, st, func(context.Context, int) error { return nil }, time.Second)
	if _, err := s.Load(spec()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(spec()); err == nil {
		t.Fatal("expected duplicate load to fail")
	}
}

func TestLoadSpawnFailure(t *testing.T) {
	st := &fakeStarter{fail: true}
	s := newSup(t, st, func(context.Context, int) error { return nil }, time.Second)
	if _, err := s.Load(spec()); err == nil {
		t.Fatal("expected spawn failure")
	}
}

func TestReadyTimeout(t *testing.T) {
	st := &fakeStarter{}
	s := newSup(t, st, func(context.Context, int) error { return errors.New("not ready") }, 150*time.Millisecond)
	if _, err := s.Load(spec()); err != nil {
		t.Fatal(err)
	}
	inst := waitState(t, s, "m1", StateError)
	if inst.Error == "" {
		t.Error("expected error message on timeout")
	}
}

func TestUnloadStopsProcess(t *testing.T) {
	st := &fakeStarter{}
	s := newSup(t, st, func(context.Context, int) error { return nil }, time.Second)
	s.Load(spec())
	waitState(t, s, "m1", StateReady)
	if err := s.Unload("m1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Get("m1"); ok {
		t.Error("instance still present after unload")
	}
	st.mu.Lock()
	proc := st.procs[0]
	st.mu.Unlock()
	if proc.signalCount() == 0 {
		t.Error("process was not signaled on unload")
	}
}

func TestUnloadMissing(t *testing.T) {
	s := newSup(t, &fakeStarter{}, nil, time.Second)
	if err := s.Unload("ghost"); err == nil {
		t.Error("expected error unloading missing instance")
	}
}

func TestProcessExitMarksError(t *testing.T) {
	st := &fakeStarter{}
	s := newSup(t, st, func(context.Context, int) error { return errors.New("not ready") }, 5*time.Second)
	if _, err := s.Load(spec()); err != nil {
		t.Fatal(err)
	}
	// Simulate the process dying.
	st.mu.Lock()
	proc := st.procs[0]
	st.mu.Unlock()
	proc.finish(errors.New("segfault"))
	inst := waitState(t, s, "m1", StateError)
	if inst.Error == "" {
		t.Error("expected error message after process exit")
	}
}

func TestOnLogCallback(t *testing.T) {
	lines := make(chan string, 8)
	st := &fakeStarter{}
	s := newSup(t, st, func(context.Context, int) error { return nil }, time.Second)
	s.SetOnLog(func(id, line string) {
		select {
		case lines <- line:
		default:
		}
	})
	if _, err := s.Load(spec()); err != nil {
		t.Fatal(err)
	}
	select {
	case line := <-lines:
		if !strings.Contains(line, "fake server starting") {
			t.Errorf("unexpected log line: %q", line)
		}
	case <-time.After(time.Second):
		t.Fatal("OnLog callback not invoked")
	}
}

func TestSetPortRange(t *testing.T) {
	st := &fakeStarter{}
	s := newSup(t, st, func(context.Context, int) error { return nil }, time.Second)
	s.SetPortRange(30500, 30502)
	inst, err := s.Load(spec())
	if err != nil {
		t.Fatal(err)
	}
	if inst.Port < 30500 || inst.Port > 30502 {
		t.Errorf("port = %d, want within 30500-30502", inst.Port)
	}
}

func TestBuildArgsManual(t *testing.T) {
	s := spec()
	s.Params = Params{Args: Args{
		{Name: "-c", Value: "8192"},
		{Name: "--flash-attn", Value: "on"},
		{Name: "-ngl", Value: "-1"},
		{Name: "--lora", Value: "/models/a.lora /models/b.lora"},
		{Name: "--alias", Value: `"my model"`},
		{Name: "--no-webui"},
		{Name: "", Value: "ignored"},
	}}
	args := BuildArgs(s, 21000)
	want := []string{
		"-m", "/models/test.gguf", "--host", "127.0.0.1", "--port", "21000",
		"-c", "8192", "--flash-attn", "on", "-ngl", "-1",
		"--lora", "/models/a.lora", "/models/b.lora",
		"--alias", "my model", "--no-webui",
	}
	if len(args) != len(want) {
		t.Fatalf("args = %v, want %v", args, want)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Errorf("arg[%d] = %q, want %q", i, args[i], want[i])
		}
	}
}

func TestBuildArgs(t *testing.T) {
	s := spec()
	s.Params = Params{Args: Args{
		{Name: "-c", Value: "8192"},
		{Name: "-ngl", Value: "99"},
		{Name: "-t", Value: "8"},
		{Name: "--no-webui"},
	}}
	args := BuildArgs(s, 21000)
	want := []string{
		"-m", "/models/test.gguf", "--host", "127.0.0.1", "--port", "21000",
		"-c", "8192", "-ngl", "99", "-t", "8", "--no-webui",
	}
	if len(args) != len(want) {
		t.Fatalf("args = %v", args)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Errorf("arg[%d] = %q, want %q", i, args[i], want[i])
		}
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
