// Command runtimeforge is the RuntimeForge daemon and CLI.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"runtimeforge/internal/appdir"
	"runtimeforge/internal/build"
	"runtimeforge/internal/config"
	"runtimeforge/internal/gguf"
	"runtimeforge/internal/hardware"
	"runtimeforge/internal/loadsettings"
	"runtimeforge/internal/models"
	"runtimeforge/internal/runtimes"
	"runtimeforge/internal/selection"
	"runtimeforge/internal/server"
	"runtimeforge/internal/source"
	"runtimeforge/internal/supervisor"
	"runtimeforge/internal/version"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "runtimeforge: "+err.Error())
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		printUsage()
		return nil
	}
	switch args[0] {
	case "version", "--version", "-v":
		fmt.Println(version.String())
		return nil
	case "help", "--help", "-h":
		printUsage()
		return nil
	case "paths":
		return cmdPaths()
	case "config":
		return cmdConfig()
	case "resolve":
		return cmdResolve(args[1:])
	case "model":
		return cmdModel(args[1:])
	case "arch":
		return cmdArch(args[1:])
	case "hardware":
		return cmdHardware()
	case "gguf":
		return cmdGGUF(args[1:])
	case "source":
		return cmdSource(args[1:])
	case "build":
		return cmdBuild(args[1:])
	case "runtime":
		return cmdRuntime(args[1:])
	case "serve":
		return cmdServe(args[1:])
	case "start":
		return cmdStart(args[1:])
	case "stop":
		return cmdStop(args[1:])
	case "status":
		return cmdStatus(args[1:])
	case "install-systemd":
		return cmdInstallSystemd(args[1:])
	default:
		return fmt.Errorf("unknown command %q (try: runtimeforge help)", args[0])
	}
}

func printUsage() {
	fmt.Fprint(os.Stderr, `RuntimeForge - build and select llama.cpp runtimes

Usage:
  runtimeforge <command> [flags]

Commands:
  serve     start the daemon (HTTP API + web UI)
  start     start the daemon in the background
  stop      stop the background daemon
  status    check whether a daemon is reachable
  install-systemd  write a systemd --user unit for the daemon
  paths     print the resolved application directories
  config    print the effective configuration as JSON
  hardware  detect GPUs and the build toolchain
  gguf      print metadata from a GGUF file
  source    manage Git runtime sources (list/add/remove/check)
  build     build a runtime from a source (cmake/ninja)
  runtime   manage built runtimes (list/remove)
  version   print version information
  help      show this help

Environment:
  RUNTIMEFORGE_HOME   override the application root (default ~/.runtimeforge)
`)
}

func resolve() (appdir.Paths, error) {
	p, err := appdir.Resolve()
	if err != nil {
		return appdir.Paths{}, err
	}
	if err := p.Ensure(); err != nil {
		return appdir.Paths{}, err
	}
	return p, nil
}

func cmdPaths() error {
	p, err := resolve()
	if err != nil {
		return err
	}
	for _, line := range []struct{ k, v string }{
		{"root", p.Root},
		{"config", p.Config},
		{"config.d", p.ConfigD},
		{"sources", p.Sources},
		{"runtimes", p.Runtimes},
		{"models", p.Models},
		{"state", p.State},
		{"logs", p.Logs},
		{"cache", p.Cache},
	} {
		fmt.Printf("%-10s %s\n", line.k+":", line.v)
	}
	return nil
}

func cmdConfig() error {
	p, err := resolve()
	if err != nil {
		return err
	}
	if _, err := config.EnsureDefaultFile(p); err != nil {
		return err
	}
	cfg, err := config.Load(p)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(cfg)
}

func cmdSource(args []string) error {
	p, err := resolve()
	if err != nil {
		return err
	}
	m, err := source.NewManager(p.Sources, p.StateFile("sources.json"), source.ExecGit{})
	if err != nil {
		return err
	}
	if err := m.EnsureDefault(); err != nil {
		return err
	}
	if len(args) == 0 {
		return errors.New("usage: runtimeforge source <list|add|remove|check>")
	}
	switch args[0] {
	case "list":
		return printJSON(m.List())
	case "add":
		if len(args) < 3 {
			return errors.New("usage: runtimeforge source add <name> <url> [ref]")
		}
		ref := ""
		if len(args) >= 4 {
			ref = args[3]
		}
		if err := m.Add(source.Source{Name: args[1], URL: args[2], Ref: ref}); err != nil {
			return err
		}
		s, _ := m.Get(args[1])
		return printJSON(s)
	case "remove":
		if len(args) < 2 {
			return errors.New("usage: runtimeforge source remove <name> [--purge]")
		}
		purge := len(args) >= 3 && args[2] == "--purge"
		return m.Remove(args[1], purge)
	case "check":
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		targets := args[1:]
		if len(targets) == 0 {
			for _, s := range m.List() {
				targets = append(targets, s.Name)
			}
		}
		results := []source.Source{}
		for _, name := range targets {
			s, err := m.Check(ctx, name)
			if err != nil {
				s.LastError = err.Error()
			}
			results = append(results, s)
		}
		return printJSON(results)
	default:
		return fmt.Errorf("unknown source subcommand %q", args[0])
	}
}

func setupEngine(p appdir.Paths) (*build.Engine, *runtimes.Registry, *source.Manager, config.Config, error) {
	cfg, err := config.Load(p)
	if err != nil {
		return nil, nil, nil, config.Config{}, err
	}
	srcMgr, err := source.NewManager(p.Sources, p.StateFile("sources.json"), source.ExecGit{})
	if err != nil {
		return nil, nil, nil, config.Config{}, err
	}
	if err := srcMgr.EnsureDefault(); err != nil {
		return nil, nil, nil, config.Config{}, err
	}
	reg := runtimes.NewRegistry(p.Runtimes)
	hw := detectHardware(cfg)
	eng := build.NewEngine(p, srcMgr, func() config.Config { return cfg }, hardware.ExecRunner{}, build.ExecRunner{})
	eng.OnSuccess = makeBuildHook(reg, srcMgr, hw)
	return eng, reg, srcMgr, cfg, nil
}

func makeBuildHook(reg *runtimes.Registry, srcMgr *source.Manager, hw hardware.Report) func(*build.Job, string) error {
	gpus := make([]string, 0, len(hw.GPUs))
	for _, g := range hw.GPUs {
		gpus = append(gpus, g.Name)
	}
	return func(j *build.Job, srcDir string) error {
		archs, err := runtimes.ExtractArchitectures(srcDir)
		if err != nil {
			return err
		}
		src, _ := srcMgr.Get(j.Source)
		binaries := map[string]string{}
		for _, name := range []string{"llama-server", "llama-cli", "llama-bench"} {
			if _, err := os.Stat(filepath.Join(j.InstallDir, "bin", name)); err == nil {
				binaries[name] = filepath.Join("bin", name)
			}
		}
		m := runtimes.Manifest{
			ID:                     runtimes.MakeID(j.Source, j.Commit, j.Backend),
			Source:                 j.Source,
			GitURL:                 src.URL,
			Ref:                    src.Ref,
			Commit:                 j.Commit,
			Backend:                j.Backend,
			Backends:               j.Backends,
			BuiltAt:                time.Now().UTC(),
			Binaries:               binaries,
			SupportedArchitectures: archs,
			HostGPUs:               gpus,
		}
		if j.Plan != nil {
			m.ResolvedCMakeFlags = append([]string{"cmake"}, j.Plan.Configure...)
			m.Env = j.Plan.Env()
		}
		return reg.Write(m)
	}
}

func cmdBuild(args []string) error {
	p, err := resolve()
	if err != nil {
		return err
	}
	eng, _, _, cfg, err := setupEngine(p)
	if err != nil {
		return err
	}

	var srcName string
	var commit string
	var backends []string
	all := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--backend":
			if i+1 >= len(args) {
				return errors.New("--backend requires a value")
			}
			i++
			backends = append(backends, args[i])
		case "--commit":
			if i+1 >= len(args) {
				return errors.New("--commit requires a value")
			}
			i++
			commit = args[i]
		case "--all":
			all = true
		default:
			if srcName != "" {
				return fmt.Errorf("unexpected argument %q", args[i])
			}
			srcName = args[i]
		}
	}
	if srcName == "" {
		return errors.New("usage: runtimeforge build <source> [--backend cuda] [--backend vulkan] [--commit SHA] [--all]")
	}
	if all {
		for name, bc := range cfg.Build.Backends {
			if bc.Enabled {
				backends = append(backends, name)
			}
		}
	}
	if len(backends) == 0 {
		backends = []string{"cpu"}
	}

	events, unsub := eng.Subscribe()
	defer unsub()

	ids := []string{}
	j, err := eng.Submit(build.Request{Source: srcName, Commit: commit, Backends: backends})
	if err != nil {
		return fmt.Errorf("submit: %w", err)
	}
	ids = append(ids, j.ID)
	fmt.Printf("queued %s (backends %s)\n", j.ID, strings.Join(j.Backends, ","))

	pending := map[string]bool{}
	for _, id := range ids {
		pending[id] = true
	}
	for len(pending) > 0 {
		ev := <-events
		if ev.Type == "log" {
			fmt.Println(ev.Line)
			continue
		}
		if ev.Job == nil {
			continue
		}
		fmt.Printf("[%s] %s %s\n", ev.Job.Backend, ev.Job.Status, ev.Job.ID)
		if ev.Job.Status == build.StatusSucceeded || ev.Job.Status == build.StatusFailed || ev.Job.Status == build.StatusCanceled {
			delete(pending, ev.Job.ID)
			if ev.Job.Error != "" {
				fmt.Printf("[%s] error: %s\n", ev.Job.Backend, ev.Job.Error)
			}
		}
	}

	failed := false
	for _, id := range ids {
		j, _ := eng.Get(id)
		if j == nil || j.Status != build.StatusSucceeded {
			failed = true
		}
	}
	if failed {
		return errors.New("one or more builds failed")
	}
	return nil
}

func cmdRuntime(args []string) error {
	p, err := resolve()
	if err != nil {
		return err
	}
	reg := runtimes.NewRegistry(p.Runtimes)
	if len(args) == 0 {
		return errors.New("usage: runtimeforge runtime <list|remove>")
	}
	switch args[0] {
	case "list":
		list, err := reg.List()
		if err != nil {
			return err
		}
		return printJSON(list)
	case "remove":
		if len(args) < 2 {
			return errors.New("usage: runtimeforge runtime remove <id>")
		}
		return reg.Remove(args[1])
	default:
		return fmt.Errorf("unknown runtime subcommand %q", args[0])
	}
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func detectHardware(cfg config.Config) hardware.Report {
	if cfg.Toolchain.SkipDetect {
		return hardware.Report{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return hardware.Detect(ctx, hardware.ExecRunner{})
}

func cmdGGUF(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: runtimeforge gguf <file.gguf>")
	}
	f, err := gguf.Open(args[0])
	if err != nil {
		return err
	}
	shard, _ := gguf.ParseShard(args[0])
	quant := gguf.QuantFromName(args[0])
	if quant == "" {
		quant = f.Quantization()
	}
	out := map[string]any{
		"path":         f.Path,
		"version":      f.Version,
		"tensor_count": f.TensorCount,
		"kv_count":     f.KVCount,
		"architecture": f.Architecture(),
		"name":         f.Name(),
		"size_label":   f.SizeLabel(),
		"quantization": quant,
		"file_type":    f.Uint(gguf.KeyFileType),
		"size_bytes":   f.SizeBytes,
	}
	if shard.Total > 0 {
		out["shard"] = map[string]any{"index": shard.Index, "total": shard.Total, "base": shard.Base}
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func cmdResolve(args []string) error {
	p, err := resolve()
	if err != nil {
		return err
	}
	if len(args) != 1 {
		return errors.New("usage: runtimeforge resolve <model-id-or-name>")
	}
	cfg, err := config.Load(p)
	if err != nil {
		return err
	}
	reg, err := models.NewRegistry(p.StateFile("models.json"))
	if err != nil {
		return err
	}
	model, ok := findModel(reg.List(), args[0])
	if !ok {
		return fmt.Errorf("model %q not found (run: runtimeforge model scan)", args[0])
	}
	rtreg := runtimes.NewRegistry(p.Runtimes)
	list, err := rtreg.List()
	if err != nil {
		return err
	}
	store, err := selection.NewStore(p.StateFile("selections.json"))
	if err != nil {
		return err
	}
	hw := detectHardware(cfg)
	res, err := selection.NewResolver(store).Resolve(
		selection.ModelRef{ID: model.ID, Architecture: model.Architecture, Format: model.Format},
		list,
		selection.Options{
			BackendPriority: cfg.Runtime.BackendPriority,
			Available:       hw.Backends,
			AutoSelect:      cfg.Runtime.AutoSelect,
		},
	)
	if err != nil {
		return err
	}
	return printJSON(map[string]any{"model": model.Name, "architecture": model.Architecture, "result": res})
}

func findModel(list []models.Model, query string) (models.Model, bool) {
	for _, m := range list {
		if m.ID == query {
			return m, true
		}
	}
	for _, m := range list {
		if strings.EqualFold(m.Name, query) {
			return m, true
		}
	}
	for _, m := range list {
		if strings.Contains(strings.ToLower(m.Name), strings.ToLower(query)) {
			return m, true
		}
	}
	return models.Model{}, false
}

func cmdModel(args []string) error {
	p, err := resolve()
	if err != nil {
		return err
	}
	cfg, err := config.Load(p)
	if err != nil {
		return err
	}
	reg, err := models.NewRegistry(p.StateFile("models.json"))
	if err != nil {
		return err
	}
	if len(args) == 0 {
		return errors.New("usage: runtimeforge model <list|scan>")
	}
	switch args[0] {
	case "list":
		return printJSON(reg.List())
	case "scan":
		dirs := append([]string(nil), cfg.Models.ScanDirs...)
		for i := 1; i < len(args); i++ {
			if args[i] == "--dir" {
				if i+1 >= len(args) {
					return errors.New("--dir requires a value")
				}
				i++
				dirs = append(dirs, args[i])
			} else {
				dirs = append(dirs, args[i])
			}
		}
		found, errs := models.Scan(dirs)
		if err := reg.Replace(found); err != nil {
			return err
		}
		for _, e := range errs {
			fmt.Fprintln(os.Stderr, "warn: "+e.Error())
		}
		return printJSON(map[string]any{
			"scanned_dirs": dirs,
			"count":        len(found),
			"errors":       len(errs),
		})
	default:
		return fmt.Errorf("unknown model subcommand %q", args[0])
	}
}

func cmdArch(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: runtimeforge arch <llama.cpp checkout>")
	}
	archs, err := runtimes.ExtractArchitectures(args[0])
	if err != nil {
		return err
	}
	return printJSON(map[string]any{"count": len(archs), "architectures": archs})
}

func cmdHardware() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rep := hardware.Detect(ctx, hardware.ExecRunner{})
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(rep)
}

// cfgState holds the live configuration so it can be updated at runtime.
// overlay carries process-level overrides (e.g. --port) that must be
// re-applied whenever the configuration is reloaded from disk.
type cfgState struct {
	mu      sync.RWMutex
	cfg     config.Config
	overlay map[string]any
}

func (c *cfgState) Get() config.Config {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cfg
}

func (c *cfgState) Set(cfg config.Config) {
	c.mu.Lock()
	c.cfg = cfg
	c.mu.Unlock()
}

func (c *cfgState) reload(p appdir.Paths) (config.Config, error) {
	c.mu.RLock()
	overlay := c.overlay
	c.mu.RUnlock()
	next, err := config.Load(p, overlay)
	if err != nil {
		return c.Get(), err
	}
	c.Set(next)
	return next, nil
}

func buildDeps(p appdir.Paths, state *cfgState) (server.Deps, func(), error) {
	cfg := state.Get()
	hw := detectHardware(cfg)

	srcMgr, err := source.NewManager(p.Sources, p.StateFile("sources.json"), source.ExecGit{})
	if err != nil {
		return server.Deps{}, nil, err
	}
	if err := srcMgr.EnsureDefault(); err != nil {
		return server.Deps{}, nil, err
	}
	rtReg := runtimes.NewRegistry(p.Runtimes)

	modelReg, err := models.NewRegistry(p.StateFile("models.json"))
	if err != nil {
		return server.Deps{}, nil, err
	}
	selStore, err := selection.NewStore(p.StateFile("selections.json"))
	if err != nil {
		return server.Deps{}, nil, err
	}
	settingsStore, err := loadsettings.NewStore(p.StateFile("model_settings.json"))
	if err != nil {
		return server.Deps{}, nil, err
	}

	eng := build.NewEngine(p, srcMgr, state.Get, hardware.ExecRunner{}, build.ExecRunner{})
	eng.OnSuccess = makeBuildHook(rtReg, srcMgr, hw)

	sup := supervisor.New(supervisor.Options{
		Paths:     p,
		PortRange: cfg.Server.InternalPortRange,
		Starter:   supervisor.ExecStarter{},
	})

	updateConfig := func(partial map[string]any) (config.Config, error) {
		if err := config.SaveOverlay(p, partial); err != nil {
			return state.Get(), err
		}
		return state.reload(p)
	}

	deps := server.Deps{
		Paths:        p,
		Config:       state.Get,
		Hardware:     func() hardware.Report { return hw },
		Sources:      srcMgr,
		Builds:       eng,
		Runtimes:     rtReg,
		Models:       modelReg,
		Selections:   selStore,
		Settings:     settingsStore,
		Supervisor:   sup,
		UpdateConfig: updateConfig,
		AutoScan:     true,
	}
	cleanup := func() { sup.Shutdown() }
	return deps, cleanup, nil
}

func cmdServe(args []string) error {
	host := ""
	port := 0
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--host":
			if i+1 >= len(args) {
				return errors.New("--host requires a value")
			}
			i++
			host = args[i]
		case "--port":
			if i+1 >= len(args) {
				return errors.New("--port requires a value")
			}
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil {
				return fmt.Errorf("invalid --port: %w", err)
			}
			port = n
		default:
			return fmt.Errorf("unknown flag %q for serve", args[i])
		}
	}

	p, err := resolve()
	if err != nil {
		return err
	}
	if _, err := config.EnsureDefaultFile(p); err != nil {
		return err
	}
	overlay := map[string]any{}
	srvCfg := map[string]any{}
	if host != "" {
		srvCfg["host"] = host
	}
	if port != 0 {
		srvCfg["port"] = port
	}
	if len(srvCfg) > 0 {
		overlay["server"] = srvCfg
	}
	cfg, err := config.Load(p, overlay)
	if err != nil {
		return err
	}
	state := &cfgState{overlay: overlay}
	state.Set(cfg)

	deps, cleanup, err := buildDeps(p, state)
	if err != nil {
		return err
	}
	defer cleanup()

	s, err := server.New(deps)
	if err != nil {
		return err
	}

	addr := net.JoinHostPort(cfg.Server.Host, strconv.Itoa(cfg.Server.Port))
	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errc := make(chan error, 1)
	go func() {
		if ln, lerr := activationListener(); lerr == nil {
			fmt.Printf("RuntimeForge %s activated via systemd socket\n", version.Version)
			fmt.Printf("app root: %s\n", p.Root)
			if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errc <- err
			}
			return
		}
		fmt.Printf("RuntimeForge %s listening on http://%s\n", version.Version, addr)
		fmt.Printf("app root: %s\n", p.Root)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		fmt.Println("\nshutting down…")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return httpSrv.Shutdown(shutdownCtx)
	}
}

func cmdStatus(args []string) error {
	host := "127.0.0.1"
	port := 1234
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--host":
			if i+1 >= len(args) {
				return errors.New("--host requires a value")
			}
			i++
			host = args[i]
		case "--port":
			if i+1 >= len(args) {
				return errors.New("--port requires a value")
			}
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil {
				return fmt.Errorf("invalid --port: %w", err)
			}
			port = n
		default:
			return fmt.Errorf("unknown flag %q for status", args[i])
		}
	}
	url := fmt.Sprintf("http://%s/api/v1/health", net.JoinHostPort(host, strconv.Itoa(port)))
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		fmt.Println("stopped")
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Printf("unhealthy (%s)\n", resp.Status)
		return nil
	}
	var body struct {
		Version string `json:"version"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	fmt.Printf("running (version %s) at %s\n", body.Version, url)
	return nil
}
