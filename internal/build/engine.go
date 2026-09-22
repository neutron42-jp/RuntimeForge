package build

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"runtimeforge/internal/appdir"
	"runtimeforge/internal/config"
	"runtimeforge/internal/source"
)

// Status is the lifecycle state of a build job.
type Status string

// Job states.
const (
	StatusQueued    Status = "queued"
	StatusRunning   Status = "running"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusCanceled  Status = "canceled"
)

// Runner executes one command, streaming combined output line by line.
type Runner interface {
	Run(ctx context.Context, dir string, env []string, name string, args []string, log func(string)) (int, error)
}

// ExecRunner is the production Runner.
type ExecRunner struct{}

// Run implements Runner.
func (ExecRunner) Run(ctx context.Context, dir string, env []string, name string, args []string, log func(string)) (int, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	// Do not leave a compiler running if the daemon is killed.
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw
	if err := cmd.Start(); err != nil {
		_ = pw.Close()
		_ = pr.Close()
		return -1, err
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		scanner := bufio.NewScanner(pr)
		scanner.Buffer(make([]byte, 64*1024), 1<<20)
		for scanner.Scan() {
			log(scanner.Text())
		}
	}()
	waitErr := cmd.Wait()
	_ = pw.Close()
	<-done
	if ctx.Err() != nil {
		return -1, ctx.Err()
	}
	if waitErr != nil {
		var ee *exec.ExitError
		if errors.As(waitErr, &ee) {
			return ee.ExitCode(), nil
		}
		return -1, waitErr
	}
	return 0, nil
}

// Job is one build attempt (one or more backends compiled together).
type Job struct {
	ID         string     `json:"id"`
	Source     string     `json:"source"`
	Commit     string     `json:"commit"`
	Backends   []string   `json:"backends"`
	Backend    string     `json:"backend"` // label, e.g. "cpu+cuda+vulkan"
	Status     Status     `json:"status"`
	QueuedAt   time.Time  `json:"queued_at"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	ExitCode   int        `json:"exit_code"`
	Error      string     `json:"error,omitempty"`
	InstallDir string     `json:"install_dir,omitempty"`
	LogPath    string     `json:"log_path,omitempty"`
	Plan       *Plan      `json:"plan,omitempty"`
	Tail       []string   `json:"tail,omitempty"`
}

// Event is broadcast to subscribers.
type Event struct {
	Type  string `json:"type"` // "job" | "log"
	JobID string `json:"job_id"`
	Job   *Job   `json:"job,omitempty"`
	Line  string `json:"line,omitempty"`
}

// Request asks for a build of one or more backends compiled together
// into a single runtime. The override fields (SPEC §6, build-job level)
// take precedence over the saved configuration for this job only and are
// all optional.
type Request struct {
	Source   string   `json:"source"`
	Ref      string   `json:"ref,omitempty"`
	Commit   string   `json:"commit,omitempty"`
	Backends []string `json:"backends"`

	CMakeDefines       map[string]string `json:"cmake_defines,omitempty"`
	ExtraConfigureArgs []string          `json:"extra_configure_args,omitempty"`
	ExtraBuildArgs     []string          `json:"extra_build_args,omitempty"`
	CC                 string            `json:"cc,omitempty"`
	CXX                string            `json:"cxx,omitempty"`
	Generator          string            `json:"generator,omitempty"`
	BuildType          string            `json:"build_type,omitempty"`
	ParallelJobs       int               `json:"parallel_jobs,omitempty"`
}

// BackendSet returns the normalized backend selection (defaults to cpu).
func (r Request) BackendSet() []string {
	set := NormalizeBackends(r.Backends)
	if len(set) == 0 {
		return []string{"cpu"}
	}
	return set
}

// Overrides reports whether any build-argument override is present.
func (r Request) Overrides() bool {
	return len(r.CMakeDefines) > 0 || len(r.ExtraConfigureArgs) > 0 ||
		len(r.ExtraBuildArgs) > 0 || r.CC != "" || r.CXX != "" ||
		r.Generator != "" || r.BuildType != "" || r.ParallelJobs != 0
}

// applyOverrides layers a request's build-argument overrides on a config.
func applyOverrides(cfg config.Config, req Request) config.Config {
	if len(req.CMakeDefines) > 0 {
		merged := map[string]string{}
		for k, v := range cfg.Build.CMakeDefines {
			merged[k] = v
		}
		for k, v := range req.CMakeDefines {
			merged[k] = v
		}
		cfg.Build.CMakeDefines = merged
	}
	cfg.Build.ExtraConfigureArgs = append(append([]string(nil), cfg.Build.ExtraConfigureArgs...), req.ExtraConfigureArgs...)
	cfg.Build.ExtraBuildArgs = append(append([]string(nil), cfg.Build.ExtraBuildArgs...), req.ExtraBuildArgs...)
	if req.Generator != "" {
		cfg.Build.Generator = req.Generator
	}
	if req.BuildType != "" {
		cfg.Build.BuildType = req.BuildType
	}
	if req.ParallelJobs != 0 {
		cfg.Build.ParallelJobs = req.ParallelJobs
	}
	if req.CC != "" {
		cfg.Build.CC = req.CC
	}
	if req.CXX != "" {
		cfg.Build.CXX = req.CXX
	}
	return cfg
}

const maxTailLines = 400

// Engine orchestrates build jobs with a concurrency limit.
type Engine struct {
	paths   appdir.Paths
	sources *source.Manager
	cfg     func() config.Config
	look    LookPather
	runner  Runner

	// OnSuccess is called after a successful build and install. It
	// receives a completed job and the source checkout directory.
	OnSuccess func(job *Job, sourceDir string) error

	mu      sync.Mutex
	jobs    map[string]*Job
	order   []string
	subs    map[int]chan Event
	subID   int
	sem     chan struct{}
	cancels map[string]context.CancelFunc
}

// NewEngine constructs a build engine.
func NewEngine(paths appdir.Paths, sources *source.Manager, cfg func() config.Config, look LookPather, runner Runner) *Engine {
	if runner == nil {
		runner = ExecRunner{}
	}
	conc := 1
	if cfg != nil {
		if n := cfg().Runtime.MaxConcurrentBuilds; n > 0 {
			conc = n
		}
	}
	return &Engine{
		paths:   paths,
		sources: sources,
		cfg:     cfg,
		look:    look,
		runner:  runner,
		jobs:    map[string]*Job{},
		subs:    map[int]chan Event{},
		sem:     make(chan struct{}, conc),
		cancels: map[string]context.CancelFunc{},
	}
}

// Subscribe returns a channel of events and a cancel function.
func (e *Engine) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 256)
	e.mu.Lock()
	id := e.subID
	e.subID++
	e.subs[id] = ch
	e.mu.Unlock()
	return ch, func() {
		e.mu.Lock()
		if c, ok := e.subs[id]; ok {
			delete(e.subs, id)
			close(c)
		}
		e.mu.Unlock()
	}
}

func (e *Engine) emit(ev Event) {
	e.mu.Lock()
	subs := make([]chan Event, 0, len(e.subs))
	for _, c := range e.subs {
		subs = append(subs, c)
	}
	e.mu.Unlock()
	for _, c := range subs {
		select {
		case c <- ev:
		default:
		}
	}
}

func (e *Engine) emitJob(j *Job) {
	e.emit(Event{Type: "job", JobID: j.ID, Job: e.snapshot(j)})
}

func (e *Engine) snapshot(j *Job) *Job {
	cp := *j
	cp.Tail = append([]string(nil), j.Tail...)
	if j.Plan != nil {
		p := *j.Plan
		cp.Plan = &p
	}
	return &cp
}

// Jobs returns all jobs, newest first.
func (e *Engine) Jobs() []*Job {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]*Job, 0, len(e.order))
	for i := len(e.order) - 1; i >= 0; i-- {
		out = append(out, e.snapshot(e.jobs[e.order[i]]))
	}
	return out
}

// Get returns a single job snapshot.
func (e *Engine) Get(id string) (*Job, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	j, ok := e.jobs[id]
	if !ok {
		return nil, false
	}
	return e.snapshot(j), true
}

// Cancel attempts to cancel a queued or running job.
func (e *Engine) Cancel(id string) bool {
	e.mu.Lock()
	cancel, ok := e.cancels[id]
	e.mu.Unlock()
	if ok {
		cancel()
		return true
	}
	return false
}

// Submit validates the request and starts a job in the background.
func (e *Engine) Submit(req Request) (*Job, error) {
	src, ok := e.sources.Get(req.Source)
	if !ok {
		return nil, fmt.Errorf("source %q not found", req.Source)
	}
	set := req.BackendSet()
	cfg := applyOverrides(e.cfg(), req)
	if missing := Preflight(cfg, set, e.look); len(missing) > 0 {
		return nil, fmt.Errorf("missing build dependencies: %s", strings.Join(missing, "; "))
	}

	j := &Job{
		ID:       newID(),
		Source:   src.Name,
		Commit:   req.Commit,
		Backends: set,
		Backend:  Label(set),
		Status:   StatusQueued,
		QueuedAt: time.Now().UTC(),
	}
	j.LogPath = filepath.Join(e.paths.Logs, "build-"+j.ID+".log")

	ctx, cancel := context.WithCancel(context.Background())
	e.mu.Lock()
	e.jobs[j.ID] = j
	e.order = append(e.order, j.ID)
	e.cancels[j.ID] = cancel
	e.mu.Unlock()
	e.emitJob(j)

	go e.run(ctx, cancel, j, req)
	return e.snapshot(j), nil
}

func (e *Engine) run(ctx context.Context, cancel context.CancelFunc, j *Job, req Request) {
	// Wait for a build slot, but honor cancellation while queued.
	select {
	case e.sem <- struct{}{}:
		defer func() { <-e.sem }()
	case <-ctx.Done():
		e.cancelJob(j.ID)
		cancel()
		e.mu.Lock()
		delete(e.cancels, j.ID)
		e.mu.Unlock()
		return
	}
	defer func() {
		cancel()
		e.mu.Lock()
		delete(e.cancels, j.ID)
		e.mu.Unlock()
	}()

	logger, closeLog := e.logSink(j)
	defer closeLog()

	now := time.Now().UTC()
	e.update(j.ID, func(j *Job) {
		j.Status = StatusRunning
		j.StartedAt = &now
	})
	e.emitJob(j)

	fail := func(msg string, code int) {
		// Surface the failure in the log stream so it is visible even
		// when the failing step produced no output of its own.
		logger("!! " + msg)
		fin := time.Now().UTC()
		e.update(j.ID, func(j *Job) {
			j.Status = StatusFailed
			j.Error = msg
			j.ExitCode = code
			j.FinishedAt = &fin
		})
		e.emitJob(j)
	}

	commit, err := e.sources.EnsureCheckout(ctx, req.Source, req.Commit)
	if err != nil {
		if ctx.Err() != nil {
			e.cancelJob(j.ID)
			return
		}
		fail("checkout: "+err.Error(), -1)
		return
	}
	e.update(j.ID, func(j *Job) { j.Commit = commit })

	srcDir := e.sources.LocalPath(req.Source)
	buildDir := filepath.Join(e.paths.Cache, "builds", req.Source, commit, j.Backend)
	installDir := filepath.Join(e.paths.Runtimes, req.Source, commit, j.Backend)

	cfg := applyOverrides(e.cfg(), req)
	plan, err := BuildPlan(cfg, req.BackendSet(), req.Source, commit, srcDir, buildDir, installDir)
	if err != nil {
		fail(err.Error(), -1)
		return
	}
	// CMake must find nvcc even when the daemon's PATH was inherited
	// before the login environment (e.g. a systemd user service started
	// at boot). Pass the resolved absolute path explicitly.
	if containsBackend(req.BackendSet(), "cuda") && cfg.Toolchain.NVCC == "" {
		if _, ok := plan.Defines["CMAKE_CUDA_COMPILER"]; !ok {
			if nvcc, err := e.look.LookPath("nvcc"); err == nil {
				plan.Configure = append(plan.Configure, "-DCMAKE_CUDA_COMPILER="+nvcc)
			}
		}
	}
	e.update(j.ID, func(j *Job) { j.Plan = &plan })
	e.emitJob(j)

	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		fail("create build dir: "+err.Error(), -1)
		return
	}
	if err := os.MkdirAll(filepath.Dir(installDir), 0o755); err != nil {
		fail("create install dir: "+err.Error(), -1)
		return
	}

	cmake := orDefault(cfg.Toolchain.CMake, "cmake")
	env := plan.Env()

	steps := []struct {
		name string
		args []string
	}{
		{"configure", plan.Configure},
		{"build", plan.Build},
	}
	for _, step := range steps {
		logger("== " + step.name + ": cmake " + strings.Join(step.args, " ") + " ==")
		code, err := e.runner.Run(ctx, buildDir, env, cmake, step.args, logger)
		if err != nil {
			if ctx.Err() != nil {
				e.cancelJob(j.ID)
				return
			}
			fail(step.name+": "+err.Error(), code)
			return
		}
		if code != 0 {
			fail(fmt.Sprintf("%s failed with exit code %d", step.name, code), code)
			return
		}
	}

	logger("== collect: " + filepath.Join(buildDir, "bin") + " -> " + filepath.Join(installDir, "bin") + " ==")
	serverBin, err := collectBinaries(buildDir, installDir)
	if err != nil {
		fail("collect: "+err.Error(), 0)
		return
	}
	if _, err := os.Stat(serverBin); err != nil {
		fail("build finished but "+serverBin+" is missing", 0)
		return
	}
	e.update(j.ID, func(j *Job) { j.InstallDir = installDir })

	if e.OnSuccess != nil {
		if err := e.OnSuccess(e.snapshot(j), srcDir); err != nil {
			fail("manifest: "+err.Error(), 0)
			return
		}
	}

	fin := time.Now().UTC()
	e.update(j.ID, func(j *Job) {
		j.Status = StatusSucceeded
		j.InstallDir = installDir
		j.FinishedAt = &fin
	})
	e.emitJob(j)
}

func (e *Engine) cancelJob(id string) {
	fin := time.Now().UTC()
	e.update(id, func(j *Job) {
		j.Status = StatusCanceled
		j.Error = "canceled"
		j.FinishedAt = &fin
	})
	e.mu.Lock()
	j := e.jobs[id]
	e.mu.Unlock()
	if j != nil {
		e.emitJob(j)
	}
}

func (e *Engine) update(id string, fn func(*Job)) {
	e.mu.Lock()
	if j, ok := e.jobs[id]; ok {
		fn(j)
	}
	e.mu.Unlock()
}

// logSink returns a logger that appends to the job tail, writes the log
// file and broadcasts log events.
func (e *Engine) logSink(j *Job) (func(string), func()) {
	var mu sync.Mutex
	f, err := os.OpenFile(j.LogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		f = nil
	}
	logf := func(line string) {
		mu.Lock()
		defer mu.Unlock()
		if f != nil {
			_, _ = io.WriteString(f, line+"\n")
		}
		e.mu.Lock()
		if job, ok := e.jobs[j.ID]; ok {
			job.Tail = append(job.Tail, line)
			if len(job.Tail) > maxTailLines {
				job.Tail = job.Tail[len(job.Tail)-maxTailLines:]
			}
		}
		e.mu.Unlock()
		e.emit(Event{Type: "log", JobID: j.ID, Line: line})
	}
	return logf, func() {
		if f != nil {
			_ = f.Close()
		}
	}
}

func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// collectBinaries copies the build tree's bin directory (executables and
// shared libraries) into the runtime's bin directory. Binaries are
// built with a $ORIGIN rpath so the result is relocatable.
func collectBinaries(buildDir, installDir string) (string, error) {
	srcDir := filepath.Join(buildDir, "bin")
	dstDir := filepath.Join(installDir, "bin")
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return "", fmt.Errorf("read build bin dir: %w", err)
	}
	if err := os.RemoveAll(dstDir); err != nil {
		return "", err
	}
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return "", err
	}
	for _, entry := range entries {
		src := filepath.Join(srcDir, entry.Name())
		dst := filepath.Join(dstDir, entry.Name())
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(src)
			if err != nil {
				return "", err
			}
			if err := os.Symlink(target, dst); err != nil {
				return "", err
			}
			continue
		}
		if err := copyFile(src, dst, info.Mode().Perm()); err != nil {
			return "", err
		}
	}
	return filepath.Join(dstDir, "llama-server"), nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
