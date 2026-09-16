package build

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"runtimeforge/internal/appdir"
	"runtimeforge/internal/config"
	"runtimeforge/internal/source"
)

type fakeLook struct {
	missing map[string]bool
}

func (f fakeLook) LookPath(file string) (string, error) {
	if f.missing[file] {
		return "", os.ErrNotExist
	}
	return "/usr/bin/" + file, nil
}

type fakeRunner struct {
	mu        sync.Mutex
	calls     [][]string
	failMatch string
}

func (f *fakeRunner) Run(_ context.Context, dir string, _ []string, name string, args []string, log func(string)) (int, error) {
	f.mu.Lock()
	f.calls = append(f.calls, append([]string{name}, args...))
	f.mu.Unlock()
	joined := strings.Join(args, " ")
	if f.failMatch != "" && strings.Contains(joined, f.failMatch) {
		log("fake failure on " + joined)
		return 1, nil
	}
	log("fake " + name + " " + joined)
	if strings.Contains(joined, "--build") {
		bin := filepath.Join(dir, "bin")
		if err := os.MkdirAll(bin, 0o755); err != nil {
			return -1, err
		}
		if err := os.WriteFile(filepath.Join(bin, "llama-server"), []byte("#!/bin/sh\n"), 0o755); err != nil {
			return -1, err
		}
		if err := os.WriteFile(filepath.Join(bin, "libllama.so.0"), []byte("lib"), 0o644); err != nil {
			return -1, err
		}
		if err := os.Symlink("libllama.so.0", filepath.Join(bin, "libllama.so")); err != nil {
			return -1, err
		}
	}
	return 0, nil
}

func (f *fakeRunner) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func initBuildRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-b", "main")
	os.WriteFile(filepath.Join(dir, "CMakeLists.txt"), []byte("# fake\n"), 0o644)
	run("add", ".")
	run("commit", "-m", "init")
	return dir
}

func newTestEngine(t *testing.T, runner Runner, look LookPather) (*Engine, appdir.Paths) {
	t.Helper()
	root := t.TempDir()
	paths := appdir.New(root)
	if err := paths.Ensure(); err != nil {
		t.Fatal(err)
	}
	sm, err := source.NewManager(paths.Sources, paths.StateFile("sources.json"), source.ExecGit{})
	if err != nil {
		t.Fatal(err)
	}
	repo := initBuildRepo(t)
	if err := sm.Add(source.Source{Name: "local", URL: repo, Ref: "main"}); err != nil {
		t.Fatal(err)
	}
	cfg := func() config.Config {
		var c config.Config
		if err := config.Decode(config.Defaults(), &c); err != nil {
			t.Fatal(err)
		}
		return c
	}
	e := NewEngine(paths, sm, cfg, look, runner)
	return e, paths
}

func waitJob(t *testing.T, e *Engine, id string) *Job {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		j, ok := e.Get(id)
		if ok && (j.Status == StatusSucceeded || j.Status == StatusFailed || j.Status == StatusCanceled) {
			return j
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("job did not finish")
	return nil
}

func TestEngineBuildSuccess(t *testing.T) {
	runner := &fakeRunner{}
	look := fakeLook{}
	e, paths := newTestEngine(t, runner, look)

	hookCalled := false
	e.OnSuccess = func(j *Job, srcDir string) error {
		hookCalled = true
		if _, err := os.Stat(filepath.Join(srcDir, "CMakeLists.txt")); err != nil {
			t.Errorf("source dir missing CMakeLists: %v", err)
		}
		return nil
	}

	j, err := e.Submit(Request{Source: "local", Backends: []string{"cpu"}})
	if err != nil {
		t.Fatal(err)
	}
	done := waitJob(t, e, j.ID)
	if done.Status != StatusSucceeded {
		t.Fatalf("status = %s err=%s", done.Status, done.Error)
	}
	if !hookCalled {
		t.Error("OnSuccess hook not called")
	}
	if runner.callCount() != 2 {
		t.Errorf("expected 2 cmake invocations (configure, build), got %d", runner.callCount())
	}
	if done.InstallDir == "" {
		t.Error("install dir empty")
	}
	server := filepath.Join(done.InstallDir, "bin", "llama-server")
	if _, err := os.Stat(server); err != nil {
		t.Errorf("llama-server not installed: %v", err)
	}
	// Symlinked shared libraries must be preserved.
	link := filepath.Join(done.InstallDir, "bin", "libllama.so")
	if target, err := os.Readlink(link); err != nil || target != "libllama.so.0" {
		t.Errorf("symlink not preserved: %q %v", target, err)
	}
	// The runtime should live under the runtimes path.
	if !strings.HasPrefix(done.InstallDir, paths.Runtimes) {
		t.Errorf("install dir %q not under %q", done.InstallDir, paths.Runtimes)
	}
	if done.Commit == "" {
		t.Error("commit not resolved")
	}
}

func TestEngineBuildConfigureFailure(t *testing.T) {
	runner := &fakeRunner{failMatch: "-S"}
	e, _ := newTestEngine(t, runner, fakeLook{})
	j, err := e.Submit(Request{Source: "local", Backends: []string{"cpu"}})
	if err != nil {
		t.Fatal(err)
	}
	done := waitJob(t, e, j.ID)
	if done.Status != StatusFailed {
		t.Fatalf("status = %s", done.Status)
	}
	if done.ExitCode != 1 {
		t.Errorf("exit code = %d", done.ExitCode)
	}
	if !strings.Contains(done.Error, "configure failed") {
		t.Errorf("error = %q", done.Error)
	}
}

func TestEnginePreflightBlocks(t *testing.T) {
	runner := &fakeRunner{}
	look := fakeLook{missing: map[string]bool{"nvcc": true}}
	e, _ := newTestEngine(t, runner, look)
	_, err := e.Submit(Request{Source: "local", Backends: []string{"cuda"}})
	if err == nil {
		t.Fatal("expected preflight error")
	}
	if !strings.Contains(err.Error(), "nvcc") {
		t.Errorf("error = %q", err)
	}
}

// blockingRunner blocks until its context is canceled, to exercise
// cancellation.
type blockingRunner struct {
	once    sync.Once
	started chan struct{}
}

func (b *blockingRunner) Run(ctx context.Context, _ string, _ []string, _ string, _ []string, log func(string)) (int, error) {
	b.once.Do(func() { close(b.started) })
	log("blocking…")
	<-ctx.Done()
	return -1, ctx.Err()
}

func TestEngineCancelRunningBuild(t *testing.T) {
	runner := &blockingRunner{started: make(chan struct{})}
	e, _ := newTestEngine(t, runner, fakeLook{})
	j, err := e.Submit(Request{Source: "local", Backends: []string{"cpu"}})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-runner.started:
	case <-time.After(3 * time.Second):
		t.Fatal("build never started")
	}
	if !e.Cancel(j.ID) {
		t.Fatal("cancel returned false")
	}
	done := waitJob(t, e, j.ID)
	if done.Status != StatusCanceled {
		t.Fatalf("status = %s (error %q), want canceled", done.Status, done.Error)
	}
}

func TestEngineCancelQueuedBuild(t *testing.T) {
	runner := &blockingRunner{started: make(chan struct{})}
	e, _ := newTestEngine(t, runner, fakeLook{})
	first, err := e.Submit(Request{Source: "local", Backends: []string{"cpu"}})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-runner.started:
	case <-time.After(3 * time.Second):
		t.Fatal("first build never started")
	}
	// Second job queues behind the first (max_concurrent_builds = 1).
	second, err := e.Submit(Request{Source: "local", Backends: []string{"cpu"}})
	if err != nil {
		t.Fatal(err)
	}
	if !e.Cancel(second.ID) {
		t.Fatal("cancel queued returned false")
	}
	done := waitJob(t, e, second.ID)
	if done.Status != StatusCanceled {
		t.Fatalf("queued job status = %s, want canceled", done.Status)
	}
	e.Cancel(first.ID)
	waitJob(t, e, first.ID)
}

func TestEngineBuildOverrides(t *testing.T) {
	runner := &fakeRunner{}
	e, _ := newTestEngine(t, runner, fakeLook{})
	j, err := e.Submit(Request{
		Source:             "local",
		Backends:           []string{"cpu"},
		CMakeDefines:       map[string]string{"GGML_NATIVE": "OFF", "MY_FLAG": "1"},
		ExtraConfigureArgs: []string{"--my-configure-arg"},
		ExtraBuildArgs:     []string{"--my-build-arg"},
		CC:                 "clang",
		CXX:                "clang++",
		BuildType:          "Debug",
		ParallelJobs:       7,
	})
	if err != nil {
		t.Fatal(err)
	}
	done := waitJob(t, e, j.ID)
	if done.Status != StatusSucceeded {
		t.Fatalf("status=%s err=%s", done.Status, done.Error)
	}
	if done.Plan == nil {
		t.Fatal("plan not recorded")
	}
	if done.Plan.CC != "clang" || done.Plan.CXX != "clang++" {
		t.Errorf("compiler override ignored: %q/%q", done.Plan.CC, done.Plan.CXX)
	}
	if done.Plan.Defines["GGML_NATIVE"] != "OFF" || done.Plan.Defines["MY_FLAG"] != "1" {
		t.Errorf("cmake overrides ignored: %v", done.Plan.Defines)
	}
	cfgJoined := strings.Join(done.Plan.Configure, " ")
	if !strings.Contains(cfgJoined, "--my-configure-arg") {
		t.Errorf("extra configure arg missing: %v", done.Plan.Configure)
	}
	if !strings.Contains(strings.Join(done.Plan.Build, " "), "--my-build-arg") {
		t.Errorf("extra build arg missing: %v", done.Plan.Build)
	}
	if done.Plan.Parallelism != 7 {
		t.Errorf("parallelism = %d", done.Plan.Parallelism)
	}
	if !strings.Contains(cfgJoined, "-DCMAKE_BUILD_TYPE=Debug") {
		t.Errorf("build type override missing: %v", done.Plan.Configure)
	}
}

func TestEngineUnknownBackendAndSource(t *testing.T) {
	e, _ := newTestEngine(t, &fakeRunner{}, fakeLook{})
	if _, err := e.Submit(Request{Source: "local", Backends: []string{"nope"}}); err == nil {
		t.Error("unknown backend should error")
	}
	if _, err := e.Submit(Request{Source: "ghost", Backends: []string{"cpu"}}); err == nil {
		t.Error("unknown source should error")
	}
}
