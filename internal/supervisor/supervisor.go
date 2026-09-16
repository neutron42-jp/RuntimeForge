// Package supervisor manages the lifecycle of llama-server child
// processes: one per explicitly loaded model (SPEC §13).
package supervisor

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"runtimeforge/internal/appdir"
)

// State is the lifecycle state of a loaded model.
type State string

// States.
const (
	StateLoading State = "loading"
	StateReady   State = "ready"
	StateError   State = "error"
	StateStopped State = "stopped"
)

// Params are llama-server launch parameters, all user overridable.
type Params struct {
	ContextSize  int      `json:"context_size,omitempty"`
	KVCacheTypeK string   `json:"cache_type_k,omitempty"`
	KVCacheTypeV string   `json:"cache_type_v,omitempty"`
	GPULayers    string   `json:"gpu_layers,omitempty"`
	Threads      int      `json:"threads,omitempty"`
	CPURange     string   `json:"cpu_range,omitempty"`
	ExtraArgs    []string `json:"extra_args,omitempty"`
}

// LoadSpec fully describes a load request.
type LoadSpec struct {
	ModelID      string `json:"model_id"`
	ModelName    string `json:"model_name"`
	ModelPath    string `json:"model_path"`
	Architecture string `json:"architecture"`
	RuntimeID    string `json:"runtime_id"`
	Backend      string `json:"backend"`
	ServerBinary string `json:"server_binary"`
	Params       Params `json:"params"`
}

// Instance is a running (or recently failed) llama-server.
type Instance struct {
	ID           string     `json:"id"`
	ModelID      string     `json:"model_id"`
	ModelName    string     `json:"model_name"`
	ModelPath    string     `json:"model_path"`
	Architecture string     `json:"architecture"`
	RuntimeID    string     `json:"runtime_id"`
	Backend      string     `json:"backend"`
	Port         int        `json:"port"`
	PID          int        `json:"pid"`
	Status       State      `json:"status"`
	StartedAt    time.Time  `json:"started_at"`
	ReadyAt      *time.Time `json:"ready_at,omitempty"`
	Error        string     `json:"error,omitempty"`
	Args         []string   `json:"args"`
	Tail         []string   `json:"tail,omitempty"`

	proc Process
}

// BaseURL is the internal llama-server address.
func (i Instance) BaseURL() string { return "http://127.0.0.1:" + strconv.Itoa(i.Port) }

// Process abstracts a child process for testing.
type Process interface {
	PID() int
	Wait() error
	Signal(os.Signal) error
	Kill() error
}

// Starter launches a child process.
type Starter interface {
	Start(name string, args, env []string, log func(string)) (Process, error)
}

// ExecStarter is the production Starter.
type ExecStarter struct{}

// Start implements Starter.
func (ExecStarter) Start(name string, args, env []string, log func(string)) (Process, error) {
	cmd := exec.Command(name, args...)
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	// Ensure the inference server is terminated if the daemon dies, even
	// when it is killed with SIGKILL.
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw
	if err := cmd.Start(); err != nil {
		_ = pw.Close()
		_ = pr.Close()
		return nil, err
	}
	go func() {
		scanner := bufio.NewScanner(pr)
		scanner.Buffer(make([]byte, 64*1024), 1<<20)
		for scanner.Scan() {
			log(scanner.Text())
		}
	}()
	p := &execProcess{cmd: cmd, done: make(chan struct{})}
	go func() {
		p.err = cmd.Wait()
		_ = pw.Close()
		close(p.done)
	}()
	return p, nil
}

type execProcess struct {
	cmd  *exec.Cmd
	done chan struct{}
	err  error
}

func (p *execProcess) PID() int { return p.cmd.Process.Pid }
func (p *execProcess) Wait() error {
	<-p.done
	return p.err
}
func (p *execProcess) Signal(sig os.Signal) error { return p.cmd.Process.Signal(sig) }
func (p *execProcess) Kill() error                { return p.cmd.Process.Kill() }

// Options configures the supervisor.
type Options struct {
	Paths        appdir.Paths
	PortRange    []int
	ReadyTimeout time.Duration
	Starter      Starter
	// HealthCheck probes readiness. Defaults to the llama-server /health
	// endpoint.
	HealthCheck func(ctx context.Context, port int) error
	// OnExit is invoked when a child process exits.
	OnExit func(id string, err error)
	// OnChange is invoked whenever an instance changes state.
	OnChange func(instance Instance)
	// OnLog is invoked for every line of llama-server output.
	OnLog func(id, line string)
}

// Supervisor tracks loaded instances.
type Supervisor struct {
	opts Options

	mu        sync.Mutex
	instances map[string]*Instance
}

// SetOnChange replaces the change callback.
func (s *Supervisor) SetOnChange(fn func(Instance)) {
	s.mu.Lock()
	s.opts.OnChange = fn
	s.mu.Unlock()
}

// SetOnLog replaces the log callback.
func (s *Supervisor) SetOnLog(fn func(id, line string)) {
	s.mu.Lock()
	s.opts.OnLog = fn
	s.mu.Unlock()
}

// SetPortRange updates the internal port range used for new loads.
func (s *Supervisor) SetPortRange(lo, hi int) {
	if hi < lo || lo <= 0 {
		return
	}
	s.mu.Lock()
	s.opts.PortRange = []int{lo, hi}
	s.mu.Unlock()
}

// New constructs a supervisor.
func New(opts Options) *Supervisor {
	if opts.Starter == nil {
		opts.Starter = ExecStarter{}
	}
	if opts.ReadyTimeout <= 0 {
		opts.ReadyTimeout = 3 * time.Minute
	}
	if len(opts.PortRange) != 2 {
		opts.PortRange = []int{20000, 20999}
	}
	if opts.HealthCheck == nil {
		opts.HealthCheck = defaultHealthCheck
	}
	return &Supervisor{opts: opts, instances: map[string]*Instance{}}
}

// List returns snapshots of all instances.
func (s *Supervisor) List() []Instance {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Instance, 0, len(s.instances))
	for _, i := range s.instances {
		out = append(out, *i)
	}
	return out
}

// Get returns a snapshot of one instance.
func (s *Supervisor) Get(id string) (Instance, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i, ok := s.instances[id]
	if !ok {
		return Instance{}, false
	}
	return *i, true
}

// Load starts a llama-server for the spec. It returns once the process
// has spawned; readiness is reported asynchronously via Status.
func (s *Supervisor) Load(spec LoadSpec) (Instance, error) {
	port, err := s.allocatePort()
	if err != nil {
		return Instance{}, err
	}

	s.mu.Lock()
	if _, exists := s.instances[spec.ModelID]; exists {
		s.mu.Unlock()
		return Instance{}, fmt.Errorf("model %q is already loaded", spec.ModelID)
	}
	args := BuildArgs(spec, port)
	inst := &Instance{
		ID:           spec.ModelID,
		ModelID:      spec.ModelID,
		ModelName:    spec.ModelName,
		ModelPath:    spec.ModelPath,
		Architecture: spec.Architecture,
		RuntimeID:    spec.RuntimeID,
		Backend:      spec.Backend,
		Port:         port,
		Status:       StateLoading,
		StartedAt:    time.Now().UTC(),
		Args:         args,
	}
	s.instances[inst.ID] = inst
	s.mu.Unlock()

	logf := s.logSink(inst.ID)
	env := []string{}
	p, err := s.opts.Starter.Start(spec.ServerBinary, args, env, logf)
	if err != nil {
		s.setError(inst.ID, err.Error())
		return Instance{}, err
	}
	inst.proc = p
	inst.PID = p.PID()
	s.notify(inst.ID)

	go s.watch(inst.ID, p)
	go s.waitReady(inst.ID)

	return s.snapshot(inst.ID)
}

// Unload stops and removes an instance.
func (s *Supervisor) Unload(id string) error {
	s.mu.Lock()
	inst, ok := s.instances[id]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("model %q is not loaded", id)
	}
	if inst.proc != nil {
		done := make(chan error, 1)
		go func() { done <- inst.proc.Wait() }()
		_ = inst.proc.Signal(os.Interrupt)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = inst.proc.Kill()
			<-done
		}
	}
	s.mu.Lock()
	delete(s.instances, id)
	s.mu.Unlock()
	if s.opts.OnChange != nil {
		stopped := *inst
		stopped.Status = StateStopped
		s.opts.OnChange(stopped)
	}
	return nil
}

// Shutdown stops every instance.
func (s *Supervisor) Shutdown() {
	for _, i := range s.List() {
		_ = s.Unload(i.ID)
	}
}

func (s *Supervisor) watch(id string, p Process) {
	err := p.Wait()
	s.mu.Lock()
	inst, ok := s.instances[id]
	var wasStopped bool
	if ok {
		wasStopped = inst.Status == StateStopped
		if !wasStopped {
			inst.Status = StateError
			if err != nil {
				inst.Error = "process exited: " + err.Error()
			} else {
				inst.Error = "process exited unexpectedly"
			}
		}
	}
	s.mu.Unlock()
	if ok && !wasStopped {
		s.notify(id)
	}
	if s.opts.OnExit != nil {
		s.opts.OnExit(id, err)
	}
}

func (s *Supervisor) waitReady(id string) {
	deadline := time.Now().Add(s.opts.ReadyTimeout)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		inst, ok := s.instances[id]
		if !ok || inst.Status != StateLoading {
			s.mu.Unlock()
			return
		}
		port := inst.Port
		s.mu.Unlock()

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := s.opts.HealthCheck(ctx, port)
		cancel()
		if err == nil {
			s.mu.Lock()
			if cur, ok := s.instances[id]; ok && cur.Status == StateLoading {
				cur.Status = StateReady
				now := time.Now().UTC()
				cur.ReadyAt = &now
			}
			s.mu.Unlock()
			s.notify(id)
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
	s.setError(id, "timed out waiting for llama-server to become ready")
}

func (s *Supervisor) setError(id, msg string) {
	s.mu.Lock()
	if inst, ok := s.instances[id]; ok {
		inst.Status = StateError
		inst.Error = msg
	}
	s.mu.Unlock()
	s.notify(id)
}

func (s *Supervisor) snapshot(id string) (Instance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	inst, ok := s.instances[id]
	if !ok {
		return Instance{}, fmt.Errorf("instance %q not found", id)
	}
	return *inst, nil
}

func (s *Supervisor) notify(id string) {
	s.mu.Lock()
	fn := s.opts.OnChange
	inst, ok := s.instances[id]
	var snap Instance
	if ok {
		snap = *inst
	}
	s.mu.Unlock()
	if fn != nil && ok {
		fn(snap)
	}
}

func (s *Supervisor) logSink(id string) func(string) {
	path := filepath.Join(s.opts.Paths.Logs, "server-"+id+".log")
	f, _ := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	return func(line string) {
		if f != nil {
			_, _ = io.WriteString(f, line+"\n")
		}
		s.mu.Lock()
		if inst, ok := s.instances[id]; ok {
			inst.Tail = append(inst.Tail, line)
			if len(inst.Tail) > 400 {
				inst.Tail = inst.Tail[len(inst.Tail)-400:]
			}
		}
		onLog := s.opts.OnLog
		s.mu.Unlock()
		if onLog != nil {
			onLog(id, line)
		}
	}
}

func (s *Supervisor) allocatePort() (int, error) {
	s.mu.Lock()
	lo, hi := s.opts.PortRange[0], s.opts.PortRange[1]
	s.mu.Unlock()
	for p := lo; p <= hi; p++ {
		s.mu.Lock()
		used := false
		for _, i := range s.instances {
			if i.Port == p {
				used = true
				break
			}
		}
		s.mu.Unlock()
		if used {
			continue
		}
		ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(p))
		if err != nil {
			continue
		}
		_ = ln.Close()
		return p, nil
	}
	return 0, errors.New("no free port available in the configured range")
}

// BuildArgs constructs the llama-server argv from a spec.
func BuildArgs(spec LoadSpec, port int) []string {
	args := []string{
		"-m", spec.ModelPath,
		"--host", "127.0.0.1",
		"--port", strconv.Itoa(port),
	}
	if spec.Params.ContextSize > 0 {
		args = append(args, "-c", strconv.Itoa(spec.Params.ContextSize))
	}
	if spec.Params.KVCacheTypeK != "" {
		args = append(args, "--cache-type-k", spec.Params.KVCacheTypeK)
	}
	if spec.Params.KVCacheTypeV != "" {
		args = append(args, "--cache-type-v", spec.Params.KVCacheTypeV)
	}
	if spec.Params.GPULayers != "" {
		args = append(args, "-ngl", spec.Params.GPULayers)
	}
	if spec.Params.Threads > 0 {
		args = append(args, "-t", strconv.Itoa(spec.Params.Threads))
	}
	if spec.Params.CPURange != "" {
		args = append(args, "--cpu-range", spec.Params.CPURange)
	}
	args = append(args, spec.Params.ExtraArgs...)
	return args
}

func defaultHealthCheck(ctx context.Context, port int) error {
	url := "http://127.0.0.1:" + strconv.Itoa(port) + "/health"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health status %s", resp.Status)
	}
	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return err
	}
	if !strings.EqualFold(body.Status, "ok") && body.Status != "ready" {
		return fmt.Errorf("model not ready: %s", body.Status)
	}
	return nil
}
