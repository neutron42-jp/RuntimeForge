// Package config implements the layered, fully overridable
// configuration described in SPEC §6. A typed Config is produced by
// merging the built-in defaults with config.toml, config.d/*.toml and
// any higher-priority layer supplied by the caller (source, backend,
// model or request overrides).
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"runtimeforge/internal/appdir"
)

// Backend holds per-backend build overrides. Empty CC/CXX inherit the
// global [build] values.
type Backend struct {
	Enabled            bool              `json:"enabled" toml:"enabled"`
	CC                 string            `json:"cc" toml:"cc"`
	CXX                string            `json:"cxx" toml:"cxx"`
	CMakeDefines       map[string]string `json:"cmake_defines" toml:"cmake_defines"`
	ExtraConfigureArgs []string          `json:"extra_configure_args" toml:"extra_configure_args"`
	ExtraBuildArgs     []string          `json:"extra_build_args" toml:"extra_build_args"`
}

// Build holds build-engine defaults.
type Build struct {
	Generator          string             `json:"generator" toml:"generator"`
	BuildType          string             `json:"build_type" toml:"build_type"`
	ParallelJobs       int                `json:"parallel_jobs" toml:"parallel_jobs"`
	CC                 string             `json:"cc" toml:"cc"`
	CXX                string             `json:"cxx" toml:"cxx"`
	CMakeDefines       map[string]string  `json:"cmake_defines" toml:"cmake_defines"`
	ExtraConfigureArgs []string           `json:"extra_configure_args" toml:"extra_configure_args"`
	ExtraBuildArgs     []string           `json:"extra_build_args" toml:"extra_build_args"`
	Backends           map[string]Backend `json:"backends" toml:"backends"`
}

// Runtime holds selection and concurrency defaults.
type Runtime struct {
	BackendPriority     []string `json:"backend_priority" toml:"backend_priority"`
	MaxConcurrentBuilds int      `json:"max_concurrent_builds" toml:"max_concurrent_builds"`
	AutoSelect          bool     `json:"auto_select" toml:"auto_select"`
}

// LoadConfig holds default llama-server launch parameters (SPEC §6).
type LoadConfig struct {
	ExtraArgs    []string `json:"extra_args" toml:"extra_args"`
	GPULayers    string   `json:"gpu_layers" toml:"gpu_layers"`
	Threads      int      `json:"threads" toml:"threads"`
	ContextSize  int      `json:"context_size" toml:"context_size"`
	KVCacheTypeK string   `json:"cache_type_k" toml:"cache_type_k"`
	KVCacheTypeV string   `json:"cache_type_v" toml:"cache_type_v"`
	CPURange     string   `json:"cpu_range" toml:"cpu_range"`
}

// Server holds HTTP server defaults.
type Server struct {
	Host              string `json:"host" toml:"host"`
	Port              int    `json:"port" toml:"port"`
	InternalPortRange []int  `json:"internal_port_range" toml:"internal_port_range"`
}

// ModelRoot is a directory to scan for models together with how deeply
// to recurse (-1 = unlimited, 0 = only the directory itself).
type ModelRoot struct {
	Path  string `json:"path" toml:"path"`
	Depth int    `json:"depth" toml:"depth"`
}

// Models holds model registry defaults.
type Models struct {
	ScanDirs  []string    `json:"scan_dirs" toml:"scan_dirs"`
	ScanDepth int         `json:"scan_depth" toml:"scan_depth"`
	Roots     []ModelRoot `json:"roots" toml:"roots"`
	Files     []string    `json:"files" toml:"files"`
}

// Toolchain holds explicit tool paths. Empty means "detect on PATH".
type Toolchain struct {
	CMake      string `json:"cmake" toml:"cmake"`
	Ninja      string `json:"ninja" toml:"ninja"`
	NVCC       string `json:"nvcc" toml:"nvcc"`
	SkipDetect bool   `json:"skip_detect" toml:"skip_detect"`
}

// Config is the fully resolved configuration.
type Config struct {
	Server    Server     `json:"server" toml:"server"`
	Runtime   Runtime    `json:"runtime" toml:"runtime"`
	Build     Build      `json:"build" toml:"build"`
	Load      LoadConfig `json:"load" toml:"load"`
	Models    Models     `json:"models" toml:"models"`
	Toolchain Toolchain  `json:"toolchain" toml:"toolchain"`
}

// Parse decodes TOML bytes into a generic map suitable for merging.
func Parse(data []byte) (map[string]any, error) {
	m := map[string]any{}
	if err := toml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse toml: %w", err)
	}
	return m, nil
}

// Defaults returns the built-in configuration as a generic map.
func Defaults() map[string]any {
	m, err := Parse([]byte(DefaultTOML))
	if err != nil {
		panic("runtimeforge: invalid built-in defaults: " + err.Error())
	}
	return m
}

// Decode converts a merged map into a typed Config.
func Decode(m map[string]any, out *Config) error {
	b, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("decode config: %w", err)
	}
	return nil
}

// Clone returns a deep copy of a generic config map.
func Clone(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = cloneValue(v)
	}
	return out
}

func cloneValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		return Clone(t)
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = cloneValue(e)
		}
		return out
	default:
		return v
	}
}

// Merge overlays overlay on top of base without mutating either input.
// Nested tables are merged recursively. A key prefixed with "+"
// appends to (rather than replaces) the corresponding base list.
func Merge(base, overlay map[string]any) map[string]any {
	out := Clone(base)
	for k, v := range overlay {
		if strings.HasPrefix(k, "+") {
			key := strings.TrimPrefix(k, "+")
			out[key] = appendValue(out[key], v)
			continue
		}
		ov, ovOK := v.(map[string]any)
		bv, bvOK := out[k].(map[string]any)
		if ovOK && bvOK {
			out[k] = Merge(bv, ov)
			continue
		}
		out[k] = cloneValue(v)
	}
	return out
}

func appendValue(existing, addition any) any {
	base, ok := existing.([]any)
	if !ok {
		base = nil
	}
	out := make([]any, 0, len(base)+1)
	out = append(out, base...)
	if add, ok := addition.([]any); ok {
		out = append(out, add...)
	} else {
		out = append(out, addition)
	}
	return out
}

// Layered returns the effective generic configuration: the built-in
// defaults merged with config.toml, config.d/*.toml (lexicographically)
// and finally the supplied overlay layers in order.
func Layered(p appdir.Paths, overlays ...map[string]any) (map[string]any, error) {
	m := Defaults()
	files, err := layerFiles(p)
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", f, err)
		}
		layer, err := Parse(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		m = Merge(m, layer)
	}
	for _, o := range overlays {
		if o == nil {
			continue
		}
		m = Merge(m, o)
	}
	// The app models directory is always a scan target.
	ensureScanDir(m, p.Models)
	return m, nil
}

func layerFiles(p appdir.Paths) ([]string, error) {
	var files []string
	if _, err := os.Stat(p.Config); err == nil {
		files = append(files, p.Config)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	entries, err := os.ReadDir(p.ConfigD)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return files, nil
		}
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, n := range names {
		files = append(files, filepath.Join(p.ConfigD, n))
	}
	return files, nil
}

func ensureScanDir(m map[string]any, dir string) {
	models, ok := m["models"].(map[string]any)
	if !ok {
		models = map[string]any{}
		m["models"] = models
	}
	list, _ := models["scan_dirs"].([]any)
	for _, e := range list {
		if s, ok := e.(string); ok && s == dir {
			return
		}
	}
	models["scan_dirs"] = append([]any{dir}, list...)
}

// Load resolves the effective typed configuration.
func Load(p appdir.Paths, overlays ...map[string]any) (Config, error) {
	m, err := Layered(p, overlays...)
	if err != nil {
		return Config{}, err
	}
	var c Config
	if err := Decode(m, &c); err != nil {
		return Config{}, err
	}
	return c, nil
}

// EnsureDefaultFile writes the built-in configuration to config.toml
// when it does not yet exist.
func EnsureDefaultFile(p appdir.Paths) (bool, error) {
	if _, err := os.Stat(p.Config); err == nil {
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(p.Config), 0o755); err != nil {
		return false, err
	}
	body := strings.TrimLeft(DefaultTOML, "\n")
	if err := os.WriteFile(p.Config, []byte(body), 0o644); err != nil {
		return false, err
	}
	return true, nil
}

// SaveOverlay merges a partial configuration into config.d/90-user.toml
// (the highest-priority user layer) and persists it. This preserves
// config.toml comments while still allowing every key to be overridden.
func SaveOverlay(p appdir.Paths, partial map[string]any) error {
	if err := os.MkdirAll(p.ConfigD, 0o755); err != nil {
		return err
	}
	path := filepath.Join(p.ConfigD, "90-user.toml")
	current := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		parsed, perr := Parse(data)
		if perr != nil {
			return perr
		}
		current = parsed
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	merged := sanitizeNumbers(Merge(current, partial))
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(merged); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// sanitizeNumbers converts integral float64 values (as produced by JSON
// decoding) into int64 so they can be written as TOML integers and read
// back into integer config fields.
func sanitizeNumbers(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = sanitizeNumbers(e)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = sanitizeNumbers(e)
		}
		return out
	case float64:
		if t == float64(int64(t)) {
			return int64(t)
		}
		return t
	default:
		return v
	}
}

// Backend returns the named backend configuration.
func (c Config) Backend(name string) (Backend, bool) {
	b, ok := c.Build.Backends[name]
	return b, ok
}

// BackendNames returns the configured backend names sorted.
func (c Config) BackendNames() []string {
	names := make([]string, 0, len(c.Build.Backends))
	for n := range c.Build.Backends {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
